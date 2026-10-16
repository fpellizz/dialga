package discover

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/decisyon/dialga/engine/internal/inspect"
	"github.com/decisyon/dialga/engine/internal/tunnel"
)

const sharedConfig = `# comment
[default]
region = eu-west-1

[profile sales]
region=us-east-1
output = json
s3 =
  region = eu-south-1

[ profile  dev ]
sso_session = corp

[sso-session corp]
region = eu-central-1

[services local]
region = us-west-2
`

// sharedCredentials holds placeholder keys: the parser must skip them.
const sharedCredentials = `[default]
aws_access_key_id = placeholder-id
[sales]
aws_secret_access_key = placeholder-secret
[legacy]
region = ap-south-1
`

func TestProfiles(t *testing.T) {
	dir := t.TempDir()
	cfgFile, credFile := filepath.Join(dir, "config"), filepath.Join(dir, "credentials")
	if err := os.WriteFile(cfgFile, []byte(sharedConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credFile, []byte(sharedCredentials), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Profiles(cfgFile, credFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []Profile{
		{Name: "default", Region: "eu-west-1"},
		{Name: "dev"},
		{Name: "legacy"}, // credentials keys are never read, region included
		{Name: "sales", Region: "us-east-1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Profiles() = %+v\nwant %+v", got, want)
	}
	if strings.Contains(strings.ToLower(strings.Join(names(got), " ")), "placeholder") {
		t.Fatal("a credentials value leaked")
	}
}

func names(list []Profile) []string {
	var out []string
	for _, p := range list {
		out = append(out, p.Name, p.Region)
	}
	return out
}

func TestProfilesMissingFiles(t *testing.T) {
	dir := t.TempDir()
	got, err := Profiles(filepath.Join(dir, "none"), filepath.Join(dir, "none-either"))
	if err != nil || len(got) != 0 {
		t.Fatalf("Profiles() = %v, %v", got, err)
	}
}

func TestProfileFiles(t *testing.T) {
	env := map[string]string{"AWS_CONFIG_FILE": "/x/config", "AWS_SHARED_CREDENTIALS_FILE": "/x/creds"}
	if c, k := ProfileFiles(func(k string) string { return env[k] }); c != "/x/config" || k != "/x/creds" {
		t.Fatalf("ProfileFiles = %s, %s", c, k)
	}
	c, k := ProfileFiles(func(string) string { return "" })
	if filepath.Base(c) != "config" || filepath.Base(k) != "credentials" {
		t.Fatalf("defaults = %s, %s", c, k)
	}
}

// fakeRDS serves two pages of instances and the option groups.
type fakeRDS struct {
	optionCalls []string
	optionErr   error
}

func (f *fakeRDS) DescribeDBInstances(_ context.Context, in *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	db := func(id, engine, og string) rdstypes.DBInstance {
		return rdstypes.DBInstance{
			DBInstanceIdentifier: aws.String(id), Engine: aws.String(engine), EngineVersion: aws.String("14.00.3500.1.v1"),
			DBInstanceClass: aws.String("db.m6i.large"), DBInstanceStatus: aws.String("available"),
			MasterUsername: aws.String("admin"),
			Endpoint:       &rdstypes.Endpoint{Address: aws.String(id + ".example.internal"), Port: aws.Int32(1433)},
			OptionGroupMemberships: []rdstypes.OptionGroupMembership{
				{OptionGroupName: aws.String(og), Status: aws.String("in-sync")},
			},
		}
	}
	if in.Marker == nil {
		return &rds.DescribeDBInstancesOutput{
			DBInstances: []rdstypes.DBInstance{
				db("zeta-sql", "sqlserver-se", "native-backup"),
				db("pg-main", "postgres", "default:postgres-16"),
				db("custom-sql", "custom-sqlserver-ee", "native-backup"),
			},
			Marker: aws.String("page-2"),
		}, nil
	}
	return &rds.DescribeDBInstancesOutput{DBInstances: []rdstypes.DBInstance{
		db("alpha-sql", "sqlserver-ex", "default:sqlserver-ex-14-00"),
		db("beta-sql", "sqlserver-se", "native-backup"),
	}}, nil
}

func (f *fakeRDS) DescribeOptionGroups(_ context.Context, in *rds.DescribeOptionGroupsInput, _ ...func(*rds.Options)) (*rds.DescribeOptionGroupsOutput, error) {
	name := aws.ToString(in.OptionGroupName)
	f.optionCalls = append(f.optionCalls, name)
	if f.optionErr != nil {
		return nil, f.optionErr
	}
	group := rdstypes.OptionGroup{OptionGroupName: in.OptionGroupName}
	if name == "native-backup" {
		group.Options = []rdstypes.Option{{OptionName: aws.String("SQLSERVER_AUDIT")}, {OptionName: aws.String("SQLSERVER_BACKUP_RESTORE")}}
	}
	return &rds.DescribeOptionGroupsOutput{OptionGroupsList: []rdstypes.OptionGroup{group}}, nil
}

func TestSQLServerInstances(t *testing.T) {
	fake := &fakeRDS{}
	got, err := SQLServerInstances(context.Background(), fake)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, inst := range got {
		ids = append(ids, inst.Identifier)
	}
	if strings.Join(ids, ",") != "alpha-sql,beta-sql,zeta-sql" {
		t.Fatalf("instances %v", ids)
	}
	if got[0].BackupRestore || !got[1].BackupRestore || !got[2].BackupRestore {
		t.Fatalf("backup option: %+v", got)
	}
	want := Instance{
		Identifier: "beta-sql", Endpoint: "beta-sql.example.internal", Port: 1433, Engine: "sqlserver-se",
		EngineVersion: "14.00.3500.1.v1", Class: "db.m6i.large", Status: "available", MasterUser: "admin",
		OptionGroup: "native-backup", BackupRestore: true,
	}
	if got[1] != want {
		t.Fatalf("instance %+v\nwant     %+v", got[1], want)
	}
	if strings.Join(fake.optionCalls, ",") != "default:sqlserver-ex-14-00,native-backup" {
		t.Fatalf("one DescribeOptionGroups per group: %v", fake.optionCalls)
	}
}

func TestSQLServerInstancesOptionError(t *testing.T) {
	_, err := SQLServerInstances(context.Background(), &fakeRDS{optionErr: errors.New("AccessDenied")})
	if err == nil || !strings.Contains(err.Error(), "option group default:sqlserver-ex-14-00: AccessDenied") {
		t.Fatalf("err %v", err)
	}
}

// lookups fakes tunnel.PluginInfo and inspect.DetectRuntime.
func lookups(pluginPath, pluginVersion string, pluginErr error, rt inspect.Runtime, rtErr error, env map[string]string) Programs {
	return Programs{
		Plugin:  func(context.Context) (string, string, error) { return pluginPath, pluginVersion, pluginErr },
		Runtime: func(context.Context) (inspect.Runtime, error) { return rt, rtErr },
		Getenv:  func(k string) string { return env[k] },
	}
}

func TestPrerequisitesFound(t *testing.T) {
	got := lookups("/usr/local/sessionmanagerplugin/bin/session-manager-plugin", "1.2.707.0", nil,
		inspect.Runtime{Name: "docker", Path: "/usr/bin/docker", Version: "28.3.1"}, nil, nil).Prerequisites(context.Background())
	plugin, containers := got[0], got[1]
	if plugin.ID != PrereqSSMPlugin || !plugin.Found || plugin.Outdated || plugin.Version != "1.2.707.0" ||
		plugin.Path != "/usr/local/sessionmanagerplugin/bin/session-manager-plugin" || plugin.Error != "" {
		t.Fatalf("plugin %+v", plugin)
	}
	if containers.ID != PrereqContainerRuntime || !containers.Found || containers.Program != "docker" || containers.Version != "28.3.1" {
		t.Fatalf("runtime %+v", containers)
	}
	if len(plugin.Install) != 2 || plugin.Install[0].Distro != "fedora" || !strings.Contains(plugin.Install[0].Command, "dnf") {
		t.Fatalf("install hints %+v", plugin.Install)
	}
}

func TestPrerequisitesPluginStates(t *testing.T) {
	missing := fmt.Errorf("%w on PATH: install it", tunnel.ErrPluginMissing)
	tests := []struct {
		name                      string
		path, version             string
		err                       error
		found, outdated, hasError bool
	}{
		{"too old for the integrated tunnel", "/usr/bin/session-manager-plugin", "1.2.497.0", nil, true, true, false},
		{"no version: found, not judged", "/usr/bin/session-manager-plugin", "", errors.New("exit status 1"), true, false, false},
		{"missing", "", "", missing, false, false, false},
		{"lookup failed otherwise", "", "", errors.New("permission denied"), false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plugin := lookups(tc.path, tc.version, tc.err, inspect.Runtime{}, errors.New("none"), nil).
				Prerequisites(context.Background())[0]
			if plugin.Found != tc.found || plugin.Outdated != tc.outdated || (plugin.Error != "") != tc.hasError {
				t.Fatalf("plugin %+v", plugin)
			}
		})
	}
}

func TestPrerequisitesRuntimeStates(t *testing.T) {
	plugin := "/usr/bin/session-manager-plugin"
	tests := []struct {
		name    string
		rt      inspect.Runtime
		err     error
		env     map[string]string
		want    Prerequisite
		wantErr string
	}{
		{"none installed: podman suggested", inspect.Runtime{}, errors.New("no container runtime"), nil,
			Prerequisite{Program: "podman"}, ""},
		{"docker daemon down", inspect.Runtime{Name: "docker", Path: "/usr/bin/docker"}, errors.New("docker info: cannot connect"), nil,
			Prerequisite{Program: "docker", Path: "/usr/bin/docker"}, "docker info: cannot connect"},
		{"forced and missing", inspect.Runtime{}, errors.New("not installed"), map[string]string{inspect.EnvRuntime: "docker"},
			Prerequisite{Program: "docker"}, ""},
		{"forced to something else", inspect.Runtime{}, errors.New("must be podman or docker"), map[string]string{inspect.EnvRuntime: "lxc"},
			Prerequisite{Program: "podman"}, "must be podman or docker"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lookups(plugin, "1.2.707.0", nil, tc.rt, tc.err, tc.env).Prerequisites(context.Background())[1]
			if got.Found || got.Program != tc.want.Program || got.Path != tc.want.Path || got.Error != tc.wantErr || len(got.Install) != 2 {
				t.Fatalf("runtime %+v", got)
			}
		})
	}
}
