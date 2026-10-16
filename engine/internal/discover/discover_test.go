package discover

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
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

func TestPrerequisites(t *testing.T) {
	programs := Programs{
		LookPath: func(file string) (string, error) {
			if file == "docker" || file == "session-manager-plugin" {
				return "/usr/bin/" + file, nil
			}
			return "", errors.New("not found")
		},
		Output: func(_ context.Context, path string, _ ...string) ([]byte, error) {
			if strings.HasSuffix(path, "docker") {
				return []byte("Docker version 28.3.1, build 38b7060\n"), nil
			}
			return nil, errors.New("exit status 1")
		},
	}
	got := programs.Prerequisites(context.Background())
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	plugin, runtime := got[0], got[1]
	if plugin.ID != PrereqSSMPlugin || !plugin.Found || plugin.Version != "" || plugin.Path != "/usr/bin/session-manager-plugin" {
		t.Fatalf("plugin %+v", plugin)
	}
	if runtime.ID != PrereqContainerRuntime || runtime.Program != "docker" || runtime.Version != "28.3.1" {
		t.Fatalf("runtime %+v", runtime)
	}
	if len(plugin.Install) != 2 || plugin.Install[0].Distro != "fedora" || !strings.Contains(plugin.Install[0].Command, "dnf") {
		t.Fatalf("install hints %+v", plugin.Install)
	}

	none := Programs{
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Output: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("ran a missing program")
			return nil, nil
		},
	}
	for _, pr := range none.Prerequisites(context.Background()) {
		if pr.Found || pr.Path != "" || len(pr.Install) != 2 {
			t.Fatalf("missing: %+v", pr)
		}
	}
	if missing := none.Prerequisites(context.Background())[1]; missing.Program != "podman" {
		t.Fatalf("a missing runtime suggests podman: %+v", missing)
	}
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]string{
		"1.2.707.0\n":                          "1.2.707.0",
		"podman version 5.6.2":                 "5.6.2",
		"Docker version 28.3.1, build 38b7060": "28.3.1",
		"no version here":                      "",
		"":                                     "",
	} {
		if got := parseVersion(in); got != want {
			t.Errorf("parseVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// programsAt finds the given files (names or absolute paths), each telling
// the given version.
func programsAt(files map[string]string, env map[string]string) Programs {
	return Programs{
		LookPath: func(file string) (string, error) {
			if _, ok := files[file]; ok {
				if strings.HasPrefix(file, "/") {
					return file, nil
				}
				return "/usr/bin/" + file, nil
			}
			return "", errors.New("not found")
		},
		Output: func(_ context.Context, path string, _ ...string) ([]byte, error) {
			for file, version := range files {
				if path == file || path == "/usr/bin/"+file {
					return []byte(version + "\n"), nil
				}
			}
			return nil, errors.New("exit status 1")
		},
		Getenv: func(k string) string { return env[k] },
		GOOS:   "linux",
	}
}

func TestPluginFallbackPathsAndAge(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		wantPath     string
		wantOutdated bool
	}{
		{"in PATH, recent", map[string]string{"session-manager-plugin": "1.2.707.0"}, "/usr/bin/session-manager-plugin", false},
		{"installer path only", map[string]string{"/usr/local/sessionmanagerplugin/bin/session-manager-plugin": "1.2.498.0"},
			"/usr/local/sessionmanagerplugin/bin/session-manager-plugin", false},
		{"too old for the integrated tunnel", map[string]string{"session-manager-plugin": "1.2.497.0"}, "/usr/bin/session-manager-plugin", true},
		{"no version: not judged", map[string]string{"session-manager-plugin": ""}, "/usr/bin/session-manager-plugin", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plugin := programsAt(tc.files, nil).Prerequisites(context.Background())[0]
			if !plugin.Found || plugin.Path != tc.wantPath || plugin.Outdated != tc.wantOutdated || plugin.Program != "session-manager-plugin" {
				t.Fatalf("plugin %+v", plugin)
			}
		})
	}
}

func TestContainerRuntimeOverride(t *testing.T) {
	both := map[string]string{"podman": "podman version 5.8.4", "docker": "Docker version 28.3.1, build x"}
	if got := programsAt(both, nil).Prerequisites(context.Background())[1]; got.Program != "podman" {
		t.Fatalf("default: %+v", got)
	}
	forced := map[string]string{EnvContainerRuntime: "docker"}
	if got := programsAt(both, forced).Prerequisites(context.Background())[1]; got.Program != "docker" || got.Version != "28.3.1" {
		t.Fatalf("forced docker: %+v", got)
	}
	onlyPodman := map[string]string{"podman": "podman version 5.8.4"}
	if got := programsAt(onlyPodman, forced).Prerequisites(context.Background())[1]; got.Found || got.Program != "docker" {
		t.Fatalf("forced docker, missing: %+v", got)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.2.497.0", "1.2.497.0", 0},
		{"1.2.498.0", "1.2.497.0", 1},
		{"1.2.50.0", "1.2.497.0", -1},
		{"1.3", "1.2.497.0", 1},
		{"1.2.497", "1.2.497.0", 0},
	} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
