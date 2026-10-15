// Package check runs the read-only pre-flight checks of a target: AWS
// credentials, RDS instance, tunnel, SQL Server login and native backup
// tasks. It is the Go counterpart of "mssql-import.sh check".
//
// Each step reports itself through an Emitter as it starts and ends, so the
// API can stream progress to the GUI. A failed step skips the ones that
// depend on it.
package check

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"

	"github.com/decisyon/dialga/engine/internal/rdsca"
)

// Request selects the target to check. The SQL password never travels in
// the request: the engine takes it from its own environment.
type Request struct {
	Profile   string `json:"profile"`
	Region    string `json:"region"`
	Instance  string `json:"instance"`
	LocalPort int    `json:"localPort"`
	SQLUser   string `json:"sqlUser"`
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
	}
	return nil
}

// Status of a step.
type Status string

const (
	Running Status = "running"
	OK      Status = "ok"
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
	stepTunnel   = step{"tunnel", "Connection"}
	stepSQL      = step{"sql", "SQL Server login (TLS verified)"}
	stepTasks    = step{"tasks", "Databases and native backup tasks"}
)

// Run executes the checks and reports whether all of them passed.
func Run(ctx context.Context, req Request, sqlPassword string, emit Emitter) bool {
	start := func(s step) { emit(Event{Step: s.id, Label: s.label, Status: Running}) }
	end := func(s step, st Status, detail string) bool {
		emit(Event{Step: s.id, Label: s.label, Status: st, Detail: detail})
		return st == OK
	}
	skip := func(s step, why string) { end(s, Skipped, why) }

	start(stepAWS)
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithSharedConfigProfile(req.Profile), config.WithRegion(req.Region))
	if err != nil {
		end(stepAWS, Failed, err.Error())
		skip(stepInstance, "needs AWS credentials")
		skip(stepTunnel, "needs the RDS instance")
		skip(stepSQL, "needs the RDS instance")
		skip(stepTasks, "needs a SQL session")
		return false
	}
	ident, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		end(stepAWS, Failed, err.Error())
		skip(stepInstance, "needs AWS credentials")
		skip(stepTunnel, "needs the RDS instance")
		skip(stepSQL, "needs the RDS instance")
		skip(stepTasks, "needs a SQL session")
		return false
	}
	account := aws.ToString(ident.Account)
	if req.ExpectedAccount != "" && account != req.ExpectedAccount {
		end(stepAWS, Failed, fmt.Sprintf("profile %s points to account %s, expected %s", req.Profile, account, req.ExpectedAccount))
		skip(stepInstance, "wrong AWS account")
		skip(stepTunnel, "wrong AWS account")
		skip(stepSQL, "wrong AWS account")
		skip(stepTasks, "wrong AWS account")
		return false
	}
	end(stepAWS, OK, fmt.Sprintf("profile %s · account %s", req.Profile, account))

	start(stepInstance)
	out, err := rds.NewFromConfig(cfg).DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(req.Instance),
	})
	if err != nil || len(out.DBInstances) == 0 || out.DBInstances[0].Endpoint == nil {
		end(stepInstance, Failed, errOr(err, "instance not found"))
		skip(stepTunnel, "needs the RDS instance")
		skip(stepSQL, "needs the RDS instance")
		skip(stepTasks, "needs a SQL session")
		return false
	}
	db := out.DBInstances[0]
	endpoint := aws.ToString(db.Endpoint.Address)
	detail := fmt.Sprintf("%s · %s %s", aws.ToString(db.DBInstanceStatus), aws.ToString(db.Engine), aws.ToString(db.EngineVersion))
	if len(db.OptionGroupMemberships) > 0 {
		detail += " · option group " + aws.ToString(db.OptionGroupMemberships[0].Status)
	}
	instanceOK := aws.ToString(db.DBInstanceStatus) == "available"
	if !instanceOK {
		end(stepInstance, Failed, detail)
	} else {
		end(stepInstance, OK, detail)
	}

	start(stepTunnel)
	endpointPort := int(aws.ToInt32(db.Endpoint.Port))
	if endpointPort == 0 {
		endpointPort = 1433
	}
	addr := sqlAddr(req, endpoint, endpointPort)
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		if req.Direct {
			end(stepTunnel, Failed, "cannot reach "+addr+": run inside the VPC or use a tunnel")
		} else {
			end(stepTunnel, Failed, "nothing listening on "+addr+": open the SSM tunnel")
		}
		skip(stepSQL, "needs a connection")
		skip(stepTasks, "needs a SQL session")
		return false
	}
	_ = conn.Close()
	if req.Direct {
		end(stepTunnel, OK, "direct · "+addr)
	} else {
		end(stepTunnel, OK, fmt.Sprintf("tunnel %s → %s:%d", addr, endpoint, endpointPort))
	}

	start(stepSQL)
	if sqlPassword == "" {
		end(stepSQL, Skipped, "no SQL password in the engine environment (DIALGA_SQL_PASSWORD)")
		skip(stepTasks, "needs a SQL session")
		return false
	}
	sqlDB, err := openSQL(req, addr, endpoint, sqlPassword)
	if err != nil {
		end(stepSQL, Failed, err.Error())
		skip(stepTasks, "needs a SQL session")
		return false
	}
	defer func() { _ = sqlDB.Close() }()
	var version, edition, login string
	err = sqlDB.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(30)),
		CAST(SERVERPROPERTY('Edition') AS nvarchar(60)), SUSER_SNAME()`).Scan(&version, &edition, &login)
	if err != nil {
		end(stepSQL, Failed, err.Error())
		skip(stepTasks, "needs a SQL session")
		return false
	}
	end(stepSQL, OK, fmt.Sprintf("%s %s · login %s · certificate verified for %s", version, edition, login, endpoint))

	start(stepTasks)
	var userDBs, running int
	err = sqlDB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM sys.databases WHERE database_id > 4 AND name <> N'rdsadmin'),
		(SELECT COUNT(*) FROM msdb.dbo.rds_fn_task_status(NULL, 0)
		   WHERE lifecycle IN (N'CREATED', N'IN_PROGRESS', N'CANCEL_REQUESTED'))`).Scan(&userDBs, &running)
	if err != nil {
		end(stepTasks, Failed, err.Error())
		return false
	}
	end(stepTasks, OK, fmt.Sprintf("%d user databases · %d native tasks running", userDBs, running))
	return instanceOK
}

// sqlAddr is where the engine opens the TDS connection: the local tunnel
// port, or the RDS endpoint itself with Direct.
func sqlAddr(req Request, endpoint string, endpointPort int) string {
	if req.Direct {
		return net.JoinHostPort(endpoint, strconv.Itoa(endpointPort))
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(req.LocalPort))
}

// openSQL connects to addr and verifies the server certificate against the
// RDS roots for the endpoint name, so the check holds through a tunnel too.
func openSQL(req Request, addr, endpoint, password string) (*sql.DB, error) {
	pool, err := rdsca.Pool()
	if err != nil {
		return nil, err
	}
	u := &url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(req.SQLUser, password),
		Host:   addr,
	}
	q := url.Values{}
	q.Set("database", "master")
	q.Set("encrypt", "true")
	q.Set("hostnameincertificate", endpoint)
	q.Set("dial timeout", "10")
	q.Set("app name", "dialga")
	u.RawQuery = q.Encode()
	cfg, err := msdsn.Parse(u.String())
	if err != nil {
		return nil, err
	}
	if cfg.TLSConfig == nil {
		return nil, errors.New("TLS configuration missing")
	}
	cfg.TLSConfig.RootCAs = pool
	cfg.TLSConfig.ServerName = endpoint
	cfg.TLSConfig.MinVersion = tls.VersionTLS12
	db := sql.OpenDB(mssql.NewConnectorConfig(cfg))
	db.SetMaxOpenConns(1)
	return db, nil
}

func errOr(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}
