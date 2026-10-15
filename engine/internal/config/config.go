// Package config loads the Dialga configuration: the RDS SQL Server targets
// the engine works on. One YAML file, shared by the CLI and the desktop app.
//
//	apiVersion: v1
//	targets:
//	  <name>:
//	    account_id: "123456789012"     # account guard: required
//	    region: eu-west-1
//	    aws_profile: default
//	    instance: my-sqlserver          # RDS DB instance identifier
//	    bucket: my-backups-bucket       # bucket of the SQLSERVER_BACKUP_RESTORE option
//	    prefixes: { import: import, export: export }
//	    protected: false
//	    max_tasks: 2
//	    sql: { user: admin, password: prompt }   # prompt | env:VAR | keyring | secretsmanager:<arn>
//	    connect: { mode: external, local_port: 11433 }  # external | direct | tunnel
//
// Unknown keys are errors, so a typo never silently falls back to a default.
// Passwords are never stored here, only where to find them.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/decisyon/dialga/engine/internal/validate"
)

// APIVersion is the only schema version this build understands.
const APIVersion = "v1"

// Connection modes.
const (
	// ModeExternal uses a tunnel that is already open on 127.0.0.1:local_port.
	ModeExternal = "external"
	// ModeDirect connects to the RDS endpoint itself (inside the VPC).
	ModeDirect = "direct"
	// ModeTunnel opens the SSM tunnel from the engine (v0.4).
	ModeTunnel = "tunnel"
)

// Password source kinds (see SQL.Password).
const (
	PasswordPrompt         = "prompt"
	PasswordEnv            = "env"
	PasswordKeyring        = "keyring"
	PasswordSecretsManager = "secretsmanager"
)

// Config is the whole file.
type Config struct {
	APIVersion string            `yaml:"apiVersion"`
	Targets    map[string]Target `yaml:"targets"`
}

// Target is one RDS SQL Server instance with its S3 bucket.
type Target struct {
	AccountID  string   `yaml:"account_id"`
	Region     string   `yaml:"region"`
	AWSProfile string   `yaml:"aws_profile"`
	Instance   string   `yaml:"instance"`
	Bucket     string   `yaml:"bucket"`
	Prefixes   Prefixes `yaml:"prefixes"`
	Protected  bool     `yaml:"protected"`
	MaxTasks   int      `yaml:"max_tasks"`
	SQL        SQL      `yaml:"sql"`
	Connect    Connect  `yaml:"connect"`
}

// Prefixes are the S3 key prefixes used for imports and exports.
type Prefixes struct {
	Import string `yaml:"import"`
	Export string `yaml:"export"`
}

// SQL is the SQL Server login of a target.
type SQL struct {
	User string `yaml:"user"`
	// Password says where the password comes from, never the password:
	// "prompt", "env:VAR", "keyring" or "secretsmanager:<secret ARN>".
	Password string `yaml:"password"`
}

// Connect says how the engine reaches SQL Server.
type Connect struct {
	Mode      string `yaml:"mode"`
	LocalPort int    `yaml:"local_port"`
	BridgeTag string `yaml:"bridge_tag"`
}

// PasswordSource is a parsed SQL.Password.
type PasswordSource struct {
	Kind string // one of the Password* constants
	Ref  string // variable name or secret ARN, empty otherwise
}

var (
	targetNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	accountPattern    = regexp.MustCompile(`^[0-9]{12}$`)
	regionPattern     = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	profilePattern    = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,64}$`)
	instancePattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,62}$`)
	bucketPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	loginPattern      = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,128}$`)
	envNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	secretARNPattern  = regexp.MustCompile(`^arn:aws[a-z-]*:secretsmanager:[a-z0-9-]+:[0-9]{12}:secret:[A-Za-z0-9/_+=.@-]+$`)
)

// DefaultPath is <user config dir>/dialga/config.yaml:
// ~/.config on Linux, %AppData% on Windows, ~/Library/Application Support on macOS.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dialga", "config.yaml"), nil
}

// Load reads, defaults and validates the file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse decodes, defaults and validates a configuration document.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if cfg.APIVersion != APIVersion {
		return nil, fmt.Errorf("apiVersion is %q, this build reads %q", cfg.APIVersion, APIVersion)
	}
	if len(cfg.Targets) == 0 {
		return nil, errors.New("no targets defined")
	}
	var errs []error
	for _, name := range cfg.Names() {
		t := cfg.Targets[name]
		t.applyDefaults()
		if err := t.validate(); err != nil {
			errs = append(errs, fmt.Errorf("target %q: %w", name, err))
		}
		if !targetNamePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("target %q: name must be lower case letters, digits and -", name))
		}
		cfg.Targets[name] = t
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Names returns the target names in alphabetical order.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Targets))
	for name := range c.Targets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Target returns the named target. An empty name selects the only target
// when there is exactly one.
func (c *Config) Target(name string) (string, Target, error) {
	if name == "" {
		if len(c.Targets) == 1 {
			name = c.Names()[0]
		} else {
			return "", Target{}, fmt.Errorf("choose a target: %s", strings.Join(c.Names(), ", "))
		}
	}
	t, ok := c.Targets[name]
	if !ok {
		return "", Target{}, fmt.Errorf("unknown target %q (known: %s)", name, strings.Join(c.Names(), ", "))
	}
	return name, t, nil
}

// Marshal renders the configuration as YAML (with defaults applied).
func (c *Config) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (t *Target) applyDefaults() {
	if t.Prefixes.Import == "" {
		t.Prefixes.Import = "import"
	}
	if t.Prefixes.Export == "" {
		t.Prefixes.Export = "export"
	}
	if t.MaxTasks == 0 {
		t.MaxTasks = 2
	}
	if t.SQL.Password == "" {
		t.SQL.Password = PasswordPrompt
	}
	if t.Connect.Mode == "" {
		t.Connect.Mode = ModeExternal
	}
}

func (t *Target) validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	check(accountPattern.MatchString(t.AccountID), "account_id must be the 12-digit AWS account (got %q)", t.AccountID)
	check(regionPattern.MatchString(t.Region), "invalid region %q", t.Region)
	check(profilePattern.MatchString(t.AWSProfile), "invalid aws_profile %q", t.AWSProfile)
	check(instancePattern.MatchString(t.Instance), "invalid instance identifier %q", t.Instance)
	check(bucketPattern.MatchString(t.Bucket), "invalid bucket name %q", t.Bucket)
	for kind, p := range map[string]string{"import": t.Prefixes.Import, "export": t.Prefixes.Export} {
		check(validate.S3Key(p) == nil && !strings.ContainsAny(p, "*") && !strings.HasSuffix(p, "/"),
			"invalid %s prefix %q", kind, p)
	}
	check(t.MaxTasks >= 1 && t.MaxTasks <= 2, "max_tasks must be 1 or 2 (RDS runs at most 2 native tasks)")
	check(loginPattern.MatchString(t.SQL.User), "invalid sql.user %q", t.SQL.User)
	if _, err := t.SQL.PasswordSource(); err != nil {
		errs = append(errs, err)
	}
	switch t.Connect.Mode {
	case ModeExternal:
		check(t.Connect.LocalPort >= 1 && t.Connect.LocalPort <= 65535,
			"connect.local_port is required with mode external (the port of the open tunnel)")
	case ModeDirect:
	case ModeTunnel:
		check(t.Connect.LocalPort >= 0 && t.Connect.LocalPort <= 65535, "invalid connect.local_port %d", t.Connect.LocalPort)
	default:
		errs = append(errs, fmt.Errorf("connect.mode must be external, direct or tunnel (got %q)", t.Connect.Mode))
	}
	return errors.Join(errs...)
}

// PasswordSource parses the password reference.
func (s SQL) PasswordSource() (PasswordSource, error) {
	kind, ref, _ := strings.Cut(s.Password, ":")
	switch kind {
	case PasswordPrompt, PasswordKeyring:
		if ref == "" {
			return PasswordSource{Kind: kind}, nil
		}
	case PasswordEnv:
		if envNamePattern.MatchString(ref) {
			return PasswordSource{Kind: kind, Ref: ref}, nil
		}
	case PasswordSecretsManager:
		if secretARNPattern.MatchString(ref) {
			return PasswordSource{Kind: kind, Ref: ref}, nil
		}
	}
	return PasswordSource{}, fmt.Errorf(
		"sql.password must be prompt, env:VAR, keyring or secretsmanager:<secret ARN> (got %q)", s.Password)
}
