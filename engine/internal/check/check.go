// Package check runs the read-only pre-flight checks of a target: AWS
// credentials, RDS instance, native backup option, S3 bucket, storage,
// connection, SQL Server login and native backup tasks. It is the Go
// counterpart of "mssql-import.sh check", with the same verdicts.
//
// Each step reports itself through an Emitter as it starts and ends, so the
// API can stream progress to the GUI. A failed step skips the ones that
// depend on it; a warning is reported but does not fail the check.
package check

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/mssql"
)

// Request selects the target to check. The SQL password never travels in
// the request: the engine takes it from its own environment.
type Request struct {
	Profile   string `json:"profile"`
	Region    string `json:"region"`
	Instance  string `json:"instance"`
	LocalPort int    `json:"localPort"`
	SQLUser   string `json:"sqlUser"`
	// Bucket is the S3 bucket of the target; empty skips its step.
	Bucket string `json:"bucket,omitempty"`
	// ExpectedAccount, when set, fails the AWS step on any other account
	// (the profile points somewhere else: stop before touching anything).
	ExpectedAccount string `json:"expectedAccount,omitempty"`
	// Direct connects to the RDS endpoint itself (inside the VPC) instead
	// of the tunnel on 127.0.0.1:LocalPort.
	Direct bool `json:"direct,omitempty"`
}

var (
	profilePattern  = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,64}$`)
	regionPattern   = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	instancePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,62}$`)
	loginPattern    = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,128}$`)
	accountPattern  = regexp.MustCompile(`^[0-9]{12}$`)
	bucketPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

// Validate rejects values that could not name a real target.
func (r Request) Validate() error {
	switch {
	case !profilePattern.MatchString(r.Profile):
		return fmt.Errorf("invalid AWS profile %q", r.Profile)
	case !regionPattern.MatchString(r.Region):
		return fmt.Errorf("invalid AWS region %q", r.Region)
	case !instancePattern.MatchString(r.Instance):
		return fmt.Errorf("invalid RDS instance identifier %q", r.Instance)
	case !r.Direct && (r.LocalPort < 1 || r.LocalPort > 65535):
		return fmt.Errorf("invalid local port %d", r.LocalPort)
	case !loginPattern.MatchString(r.SQLUser):
		return fmt.Errorf("invalid SQL login %q", r.SQLUser)
	case r.ExpectedAccount != "" && !accountPattern.MatchString(r.ExpectedAccount):
		return fmt.Errorf("invalid expected account %q", r.ExpectedAccount)
	case r.Bucket != "" && !bucketPattern.MatchString(r.Bucket):
		return fmt.Errorf("invalid S3 bucket name %q", r.Bucket)
	}
	return nil
}

// Status of a step.
type Status string

const (
	Running Status = "running"
	OK      Status = "ok"
	// Warning is worth reading but does not fail the check.
	Warning Status = "warning"
	Failed  Status = "failed"
	Skipped Status = "skipped"
)

// Event reports the state of one step.
type Event struct {
	Step   string `json:"step"`
	Label  string `json:"label"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Emitter receives the events of a run, in order.
type Emitter func(Event)

type step struct {
	id, label string
}

var (
	stepAWS      = step{"aws", "AWS credentials"}
	stepInstance = step{"instance", "RDS instance"}
	stepOption   = step{"option", "Native backup option"}
	stepBucket   = step{"bucket", "S3 bucket"}
	stepStorage  = step{"storage", "Storage"}
	stepTunnel   = step{"tunnel", "Connection"}
	stepSQL      = step{"sql", "SQL Server login (TLS verified)"}
	stepTasks    = step{"tasks", "Databases and native backup tasks"}
)

const gib = 1 << 30

// reporter emits the events of a run and remembers whether a step failed.
type reporter struct {
	emit   Emitter
	failed bool
}

func (r *reporter) start(s step) { r.emit(Event{Step: s.id, Label: s.label, Status: Running}) }

func (r *reporter) end(s step, st Status, detail string) {
	if st == Failed {
		r.failed = true
	}
	r.emit(Event{Step: s.id, Label: s.label, Status: st, Detail: detail})
}

func (r *reporter) skip(why string, steps ...step) {
	for _, s := range steps {
		r.end(s, Skipped, why)
	}
}

// Run executes the checks and reports whether none of them failed.
func Run(ctx context.Context, req Request, sqlPassword string, emit Emitter) bool {
	r := &reporter{emit: emit}

	r.start(stepAWS)
	cfg, account, err := awsx.Load(ctx, req.Profile, req.Region, req.ExpectedAccount)
	if wrong := (*awsx.WrongAccountError)(nil); errors.As(err, &wrong) {
		r.end(stepAWS, Failed, err.Error())
		r.skip("wrong AWS account", stepInstance, stepOption, stepBucket, stepStorage, stepTunnel, stepSQL, stepTasks)
		return false
	}
	if err != nil {
		r.end(stepAWS, Failed, err.Error())
		r.skip("needs AWS credentials", stepInstance, stepOption, stepBucket, stepStorage, stepTunnel, stepSQL, stepTasks)
		return false
	}
	r.end(stepAWS, OK, fmt.Sprintf("profile %s · account %s", req.Profile, account))

	r.start(stepInstance)
	inst, err := awsx.DescribeInstance(ctx, cfg, req.Instance)
	if err != nil {
		r.end(stepInstance, Failed, err.Error())
		r.skip("needs the RDS instance", stepOption)
		checkBucket(ctx, cfg, req, r)
		r.skip("needs the RDS instance", stepStorage, stepTunnel, stepSQL, stepTasks)
		return false
	}
	detail := fmt.Sprintf("%s · %s %s", inst.Status, inst.Engine, inst.EngineVersion)
	if inst.OptionGroup != "" {
		detail += fmt.Sprintf(" · option group %s %s", inst.OptionGroup, inst.OptionGroupStatus)
	}
	if inst.Status == "available" && (inst.OptionGroup == "" || inst.OptionGroupStatus == "in-sync") {
		r.end(stepInstance, OK, detail)
	} else {
		r.end(stepInstance, Failed, detail)
	}

	checkOption(ctx, cfg, inst, r)
	checkBucket(ctx, cfg, req, r)
	checkStorage(ctx, cfg, req, inst, r)

	r.start(stepTunnel)
	addr := mssql.Addr(req.Direct, req.LocalPort, inst.Endpoint, inst.Port)
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		if req.Direct {
			r.end(stepTunnel, Failed, "cannot reach "+addr+": run inside the VPC or use a tunnel")
		} else {
			r.end(stepTunnel, Failed, "nothing listening on "+addr+": open the SSM tunnel")
		}
		r.skip("needs a connection", stepSQL)
		r.skip("needs a SQL session", stepTasks)
		return false
	}
	_ = conn.Close()
	if req.Direct {
		r.end(stepTunnel, OK, "direct · "+addr)
	} else {
		r.end(stepTunnel, OK, fmt.Sprintf("tunnel %s → %s:%d", addr, inst.Endpoint, inst.Port))
	}

	r.start(stepSQL)
	if sqlPassword == "" {
		r.end(stepSQL, Skipped, "no SQL password in the engine environment (DIALGA_SQL_PASSWORD)")
		r.skip("needs a SQL session", stepTasks)
		return false
	}
	sqlDB, err := mssql.Open(addr, inst.Endpoint, req.SQLUser, sqlPassword)
	if err != nil {
		r.end(stepSQL, Failed, err.Error())
		r.skip("needs a SQL session", stepTasks)
		return false
	}
	defer func() { _ = sqlDB.Close() }()
	var version, edition, login string
	err = sqlDB.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(30)),
		CAST(SERVERPROPERTY('Edition') AS nvarchar(60)), SUSER_SNAME()`).Scan(&version, &edition, &login)
	if err != nil {
		r.end(stepSQL, Failed, err.Error())
		r.skip("needs a SQL session", stepTasks)
		return false
	}
	r.end(stepSQL, OK, fmt.Sprintf("%s %s · login %s · certificate verified for %s", version, edition, login, inst.Endpoint))

	r.start(stepTasks)
	var userDBs, running int
	err = sqlDB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM sys.databases WHERE database_id > 4 AND name <> N'rdsadmin'),
		(SELECT COUNT(*) FROM msdb.dbo.rds_fn_task_status(NULL, 0)
		   WHERE lifecycle IN (N'CREATED', N'IN_PROGRESS', N'CANCEL_REQUESTED'))`).Scan(&userDBs, &running)
	switch {
	case err != nil:
		r.end(stepTasks, Failed, err.Error())
	case running > 0:
		r.end(stepTasks, Warning, fmt.Sprintf(
			"%d user databases · %d native tasks running or queued: a new one may wait (dialga status)", userDBs, running))
	default:
		r.end(stepTasks, OK, fmt.Sprintf("%d user databases · no native task running", userDBs))
	}
	return !r.failed
}

// checkOption: native backup and restore need SQLSERVER_BACKUP_RESTORE in
// the option group of the instance.
func checkOption(ctx context.Context, cfg aws.Config, inst awsx.Instance, r *reporter) {
	r.start(stepOption)
	if inst.OptionGroup == "" {
		r.end(stepOption, Failed, "the instance has no option group: native backup and restore cannot run")
		return
	}
	has, err := awsx.HasOption(ctx, cfg, inst.OptionGroup, awsx.NativeBackupOption)
	switch {
	case err != nil:
		r.end(stepOption, Failed, err.Error())
	case !has:
		r.end(stepOption, Failed, fmt.Sprintf("%s missing in option group %s: native backup and restore cannot run",
			awsx.NativeBackupOption, inst.OptionGroup))
	default:
		r.end(stepOption, OK, fmt.Sprintf("%s in %s · automated backups %s UTC: avoid long restores across it",
			awsx.NativeBackupOption, inst.OptionGroup, inst.BackupWindow))
	}
}

func checkBucket(ctx context.Context, cfg aws.Config, req Request, r *reporter) {
	r.start(stepBucket)
	if req.Bucket == "" {
		r.end(stepBucket, Skipped, "no bucket in the configuration")
		return
	}
	if err := awsx.HeadBucket(ctx, cfg, req.Bucket); err != nil {
		r.end(stepBucket, Failed, fmt.Sprintf("s3://%s not reachable: %v", req.Bucket, err))
		return
	}
	r.end(stepBucket, OK, fmt.Sprintf("s3://%s reachable", req.Bucket))
}

// checkStorage reports the space; without CloudWatch it only warns.
func checkStorage(ctx context.Context, cfg aws.Config, req Request, inst awsx.Instance, r *reporter) {
	r.start(stepStorage)
	alloc := fmt.Sprintf("allocated %d GiB", inst.AllocatedGiB)
	if inst.MaxAllocatedGiB > 0 {
		alloc += fmt.Sprintf(" (autoscaling up to %d GiB)", inst.MaxAllocatedGiB)
	}
	free, err := awsx.FreeStorageBytes(ctx, cfg, req.Instance)
	if err != nil {
		r.end(stepStorage, Warning, "free space not read from CloudWatch (permissions?) · "+alloc)
		return
	}
	r.end(stepStorage, OK, fmt.Sprintf(
		"%s · free %.1f GiB · a restored database takes the sum of its files, not the .bak size", alloc, free/gib))
}
