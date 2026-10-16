package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/decisyon/dialga/engine/internal/config"
	"github.com/decisyon/dialga/engine/internal/discover"
)

// Defaults of a new target.
const (
	defaultRegion    = "us-east-1"
	defaultLocalPort = 11433 // the dev-mssql port of open-dloop-tunnels.sh
)

// stdinIsTerminal reports whether the config commands can ask questions;
// tests replace it.
var stdinIsTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (a *app) discovery() discover.Service {
	if a.discover != nil {
		return a.discover
	}
	return discover.System{Getenv: a.getenv}
}

// targetFlags are the fields of a target on the command line.
type targetFlags struct {
	profile, region, accountID, instance, bucket, sqlUser, password, mode string
	localPort                                                             int
	protected, replace                                                    bool
}

func (f *targetFlags) register(cmd *cobra.Command, withAccount bool) {
	fl := cmd.Flags()
	fl.StringVar(&f.profile, "profile", "", "AWS profile (default: $AWS_PROFILE, else default)")
	fl.StringVar(&f.region, "region", "", "AWS region (default: the region of the profile, else "+defaultRegion+")")
	if withAccount {
		fl.StringVar(&f.accountID, "account-id", "", "AWS account of the instance (12 digits): the account guard")
	}
	fl.StringVar(&f.instance, "instance", "", "RDS DB instance identifier")
	fl.StringVar(&f.bucket, "bucket", "", "S3 bucket of the SQLSERVER_BACKUP_RESTORE option")
	fl.StringVar(&f.sqlUser, "sql-user", "", "SQL Server login")
	fl.StringVar(&f.password, "password", "", "where the SQL password comes from: prompt or env:VAR (default prompt), never the password")
	fl.StringVar(&f.mode, "mode", "", "connection: tunnel (Dialga opens the SSM tunnel), external (a tunnel open on --local-port) or direct (default tunnel)")
	fl.IntVar(&f.localPort, "local-port", 0, fmt.Sprintf("port of the open tunnel with --mode external (default %d)", defaultLocalPort))
	fl.BoolVar(&f.protected, "protected", false, "every change on the instance asks to retype the target name")
	fl.BoolVar(&f.replace, "replace", false, "replace the target with the same name")
}

func (a *app) defaultProfile() string {
	if p := a.getenv("AWS_PROFILE"); p != "" {
		return p
	}
	return "default"
}

func profileRegion(profiles []discover.Profile, name string) string {
	for _, p := range profiles {
		if p.Name == name && p.Region != "" {
			return p.Region
		}
	}
	return defaultRegion
}

func hasProfile(profiles []discover.Profile, name string) bool {
	return slices.ContainsFunc(profiles, func(p discover.Profile) bool { return p.Name == name })
}

// suggestName turns an instance identifier into a target name.
func suggestName(identifier string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(identifier) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// savedTarget is the JSON answer of config init and add-target.
type savedTarget struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Replaced bool   `json:"replaced"`
}

// saveTarget writes the target name: change gets the existing target when
// replacing (so the keys no flag sets keep their values), a zero one
// otherwise.
func (a *app) saveTarget(cmd *cobra.Command, path, name string, replace bool, change func(config.Target) config.Target) error {
	_, statErr := os.Stat(path)
	replaced := false
	err := config.Update(path, func(c *config.Config) error {
		base, exists := c.Targets[name]
		if err := c.SetTarget(name, change(base), replace); err != nil {
			return err
		}
		replaced = exists
		return nil
	})
	if errors.Is(err, config.ErrTargetExists) {
		return fmt.Errorf("%w (use --replace to change it)", err)
	}
	if err != nil {
		return err
	}
	if a.output == "json" {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(savedTarget{path, name, replaced})
	}
	w := cmd.OutOrStdout()
	verb := "Added"
	if replaced {
		verb = "Replaced"
	}
	_, _ = fmt.Fprintf(w, "%s target %s in %s", verb, name, path)
	if statErr == nil {
		_, _ = fmt.Fprintf(w, " (previous version: %s.bak)", path)
	}
	_, err = fmt.Fprintf(w, ".\nNext: dialga check %s\n", name)
	return err
}

func (a *app) configInitCmd() *cobra.Command {
	var f targetFlags
	var name string
	var noBucketCheck, yes bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Add a target, guided: AWS profile, instance discovery, bucket, SQL login",
		Long: `Add a target to the configuration, guided: AWS profile, region, a SQL Server
instance found in the account, the S3 bucket (checked with HeadBucket), the
SQL login, where its password comes from (never the password), connection
mode and target name.

On a terminal it asks for every value the flags do not give. Otherwise the
values come from the flags, --instance and --bucket at least.

AWS calls, all read-only: sts:GetCallerIdentity (the account guard),
rds:DescribeDBInstances, rds:DescribeOptionGroups, s3:HeadBucket.

The file is written atomically with mode 0600. Comments are not kept: the
previous version stays next to it as .bak.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runInit(cmd, &f, name, noBucketCheck, yes)
		},
	}
	f.register(cmd, false)
	cmd.Flags().StringVar(&name, "name", "", "target name (default: from the instance identifier)")
	cmd.Flags().BoolVar(&noBucketCheck, "no-bucket-check", false, "keep the bucket even if HeadBucket fails")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "save without asking for a confirmation")
	return cmd
}

func (a *app) runInit(cmd *cobra.Command, f *targetFlags, name string, noBucketCheck, yes bool) error {
	ctx, errOut := cmd.Context(), cmd.ErrOrStderr()
	set := cmd.Flags().Changed
	path, err := a.configFile()
	if err != nil {
		return err
	}
	cfg, err := config.LoadOrEmpty(path)
	if err != nil {
		return err
	}
	var p *prompter
	if stdinIsTerminal(a.stdin) {
		p = &prompter{in: bufio.NewReader(a.stdin), out: errOut}
	} else if !set("instance") || !set("bucket") {
		return errors.New("not a terminal: give at least --instance and --bucket")
	}
	d := a.discovery()

	// 1. AWS profile and region.
	profiles, err := d.Profiles()
	if err != nil {
		return fmt.Errorf("AWS profiles: %w", err)
	}
	profile := f.profile
	if !set("profile") {
		profile = a.defaultProfile()
		if p != nil {
			if len(profiles) == 0 {
				return errors.New("no AWS profiles on this computer: create one with aws configure (or aws configure sso)")
			}
			keys := make([]string, len(profiles))
			rows := make([]string, len(profiles))
			for i, pr := range profiles {
				keys[i], rows[i] = pr.Name, pr.Name
				if pr.Region != "" {
					rows[i] += " (" + pr.Region + ")"
				}
			}
			i, err := p.choose("AWS profile", keys, rows, max(slices.Index(keys, profile), 0))
			if err != nil {
				return err
			}
			profile = keys[i]
		}
	}
	if !config.ValidProfile(profile) {
		return fmt.Errorf("invalid AWS profile %q", profile)
	}
	if !hasProfile(profiles, profile) && profile != "default" {
		return fmt.Errorf("AWS profile %q is not in the AWS shared files (known: %s)", profile, profileNames(profiles))
	}
	region := f.region
	if !set("region") {
		region = profileRegion(profiles, profile)
		if p != nil {
			if region, err = p.ask("AWS region", region, validRegion); err != nil {
				return err
			}
		}
	}
	if err := validRegion(region); err != nil {
		return err
	}

	// 2. Instances the profile sees, and the account it points to.
	_, _ = fmt.Fprintf(errOut, "Looking up SQL Server instances (profile %s, %s)...\n", profile, region)
	found, err := d.Instances(ctx, profile, region)
	if err != nil {
		return fmt.Errorf("instance discovery: %w", err)
	}
	_, _ = fmt.Fprintf(errOut, "Account %s: %d SQL Server instance(s).\n", found.AccountID, len(found.Instances))
	if len(found.Instances) == 0 {
		return fmt.Errorf("no RDS SQL Server instance in account %s, %s", found.AccountID, region)
	}
	keys := make([]string, len(found.Instances))
	for i, inst := range found.Instances {
		keys[i] = inst.Identifier
	}
	index := slices.Index(keys, f.instance)
	switch {
	case set("instance") && index < 0:
		return fmt.Errorf("instance %q not found (found: %s)", f.instance, strings.Join(keys, ", "))
	case !set("instance"):
		rows := make([]string, len(found.Instances))
		for i, inst := range found.Instances {
			rows[i] = instanceRow(inst)
		}
		if index, err = p.choose("Instance", keys, rows, 0); err != nil {
			return err
		}
	}
	inst := found.Instances[index]
	if !inst.BackupRestore {
		_, _ = fmt.Fprintf(errOut, "warning: option group %q has no %s: native backup and restore fail until it is added.\n",
			inst.OptionGroup, "SQLSERVER_BACKUP_RESTORE")
	}
	if inst.Status != "available" {
		_, _ = fmt.Fprintf(errOut, "warning: instance %s is %s, not available.\n", inst.Identifier, inst.Status)
	}

	// 3. Bucket, checked with HeadBucket.
	bucket, err := a.initBucket(cmd, p, f.bucket, profile, region, noBucketCheck)
	if err != nil {
		return err
	}

	// 4. SQL login and where its password comes from.
	sqlUser := f.sqlUser
	if !set("sql-user") {
		sqlUser = inst.MasterUser
		if p != nil {
			if sqlUser, err = p.ask("SQL login", sqlUser, nil); err != nil {
				return err
			}
		}
	}
	password := f.password
	if !set("password") {
		password = config.PasswordPrompt
		if p != nil {
			_, _ = fmt.Fprintln(errOut, "Where the SQL password comes from (never the password): prompt asks (terminal or app)"+
				" or reads DIALGA_SQL_PASSWORD, env:VAR reads the variable VAR, keyring the system keyring"+
				" (dialga password set), secretsmanager:<ARN> AWS Secrets Manager.")
			if password, err = p.ask("SQL password source", password, config.SupportedPasswordSource); err != nil {
				return err
			}
		}
	}
	if err := config.SupportedPasswordSource(password); err != nil {
		return err
	}

	// 5. Connection.
	mode, port := f.mode, f.localPort
	if !set("mode") {
		mode = config.ModeTunnel
		if p != nil {
			modes := []string{config.ModeTunnel, config.ModeExternal, config.ModeDirect}
			i, err := p.choose("Connection", modes, []string{
				"tunnel: Dialga opens the SSM tunnel (recommended)",
				"external: a tunnel already open on 127.0.0.1 (open-dloop-tunnels.sh)",
				"direct: the RDS endpoint itself (from inside the VPC)",
			}, 0)
			if err != nil {
				return err
			}
			mode = modes[i]
		}
	}
	if mode == config.ModeExternal && !set("local-port") {
		port = defaultLocalPort
		if p != nil {
			answer, err := p.ask("Local port of the tunnel", strconv.Itoa(port), validPort)
			if err != nil {
				return err
			}
			port, _ = strconv.Atoi(answer)
		}
	}

	// 6. Target name and protection.
	replace := f.replace
	if !set("name") {
		name = suggestName(inst.Identifier)
	}
	for p != nil && !set("name") {
		if name, err = p.ask("Target name", name, validTargetName); err != nil {
			return err
		}
		if _, exists := cfg.Targets[name]; !exists || replace {
			break
		}
		if replace, err = p.confirm(fmt.Sprintf("Target %s exists: replace it?", name), false); err != nil {
			return err
		}
		if replace {
			break
		}
	}
	protected := f.protected
	if !set("protected") && p != nil {
		guess := strings.Contains(name, "prod") || strings.Contains(inst.Identifier, "prod")
		if protected, err = p.confirm("Protect it (every change on the instance asks to retype the target name)?", guess); err != nil {
			return err
		}
	}

	t := config.Target{
		AccountID: found.AccountID, Region: region, AWSProfile: profile, Instance: inst.Identifier, Bucket: bucket,
		SQL: config.SQL{User: sqlUser, Password: password}, Connect: config.Connect{Mode: mode, LocalPort: port},
		Protected: protected,
	}
	if _, err := config.Check(name, t); err != nil {
		return err
	}
	if p != nil && !yes {
		printTarget(errOut, name, t, inst)
		ok, err := p.confirm("Save it to "+path+"?", true)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("not saved")
		}
	}
	return a.saveTarget(cmd, path, name, replace, func(base config.Target) config.Target {
		base.AccountID, base.Region, base.AWSProfile, base.Instance, base.Bucket = t.AccountID, t.Region, t.AWSProfile, t.Instance, t.Bucket
		base.SQL, base.Protected = t.SQL, t.Protected
		base.Connect.Mode, base.Connect.LocalPort = t.Connect.Mode, t.Connect.LocalPort
		return base
	})
}

// otherBucket is the last row of the bucket choice: type the name.
const otherBucket = "other (type it)"

// pickBucket offers the buckets of the region as a numbered choice plus
// "other (type it)". It returns "" when the user wants to type the name, or
// when there is nothing to choose from (no s3:ListAllMyBuckets, no buckets):
// the listing is a convenience, never an obstacle.
func (a *app) pickBucket(cmd *cobra.Command, p *prompter, current, profile, region string) (string, error) {
	list, err := a.discovery().Buckets(cmd.Context(), profile, region)
	switch {
	case errors.Is(err, discover.ErrBucketListDenied):
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "note: %v.\n", err)
		return "", nil
	case err != nil:
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "note: cannot list the buckets (%v): type the bucket name.\n", err)
		return "", nil
	case len(list) == 0:
		return "", nil
	}
	keys := make([]string, 0, len(list)+1)
	rows := make([]string, 0, len(list)+1)
	for _, b := range list {
		keys, rows = append(keys, b.Name), append(rows, b.Name)
	}
	keys, rows = append(keys, otherBucket), append(rows, otherBucket)
	def := slices.Index(keys, current)
	if def < 0 {
		def = 0
	}
	i, err := p.choose("S3 bucket (of the SQLSERVER_BACKUP_RESTORE option)", keys, rows, def)
	if err != nil || keys[i] == otherBucket {
		return "", err
	}
	return keys[i], nil
}

// initBucket asks for the bucket (on a terminal: from the listed buckets, or
// typed) and checks it with HeadBucket: a failure asks again, or is an error
// without a terminal, unless noCheck.
func (a *app) initBucket(cmd *cobra.Command, p *prompter, bucket, profile, region string, noCheck bool) (string, error) {
	errOut := cmd.ErrOrStderr()
	for attempt := 0; ; attempt++ {
		if p != nil && (attempt > 0 || !cmd.Flags().Changed("bucket")) {
			var err error
			picked := ""
			if attempt == 0 {
				if picked, err = a.pickBucket(cmd, p, bucket, profile, region); err != nil {
					return "", err
				}
			}
			if picked != "" {
				bucket = picked
			} else if bucket, err = p.ask("S3 bucket (of the SQLSERVER_BACKUP_RESTORE option)", bucket, validBucket); err != nil {
				return "", err
			}
		}
		if err := validBucket(bucket); err != nil {
			return "", err
		}
		if noCheck {
			return bucket, nil
		}
		err := a.discovery().CheckBucket(cmd.Context(), profile, region, bucket)
		if err == nil {
			_, _ = fmt.Fprintf(errOut, "Bucket %s: reachable.\n", bucket)
			return bucket, nil
		}
		if p == nil {
			return "", fmt.Errorf("bucket %s: %w (--no-bucket-check keeps it anyway)", bucket, err)
		}
		_, _ = fmt.Fprintf(errOut, "Bucket %s: %v\n", bucket, err)
		keep, cerr := p.confirm("Keep it anyway?", false)
		if cerr != nil {
			return "", cerr
		}
		if keep {
			return bucket, nil
		}
		if attempt+1 >= maxAttempts {
			return "", fmt.Errorf("bucket %s: %w", bucket, err)
		}
	}
}

func instanceRow(inst discover.Instance) string {
	option := "native backup option: yes"
	if !inst.BackupRestore {
		option = "native backup option: MISSING"
	}
	return fmt.Sprintf("%s  %s %s  %s  %s  %s", inst.Identifier, inst.Engine, inst.EngineVersion, inst.Class, inst.Status, option)
}

func printTarget(w io.Writer, name string, t config.Target, inst discover.Instance) {
	connection := t.Connect.Mode
	if t.Connect.Mode == config.ModeExternal {
		connection += fmt.Sprintf(", 127.0.0.1:%d", t.Connect.LocalPort)
	}
	protected := "no"
	if t.Protected {
		protected = "yes"
	}
	_, _ = fmt.Fprintf(w, "\nTarget %s\n  account     %s (profile %s, %s)\n  instance    %s (%s %s, %s)\n"+
		"  bucket      %s\n  SQL login   %s, password: %s\n  connection  %s\n  protected   %s\n\n",
		name, t.AccountID, t.AWSProfile, t.Region, t.Instance, inst.Engine, inst.EngineVersion, inst.Status,
		t.Bucket, t.SQL.User, t.SQL.Password, connection, protected)
}

func profileNames(profiles []discover.Profile) string {
	if len(profiles) == 0 {
		return "none"
	}
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}

func validRegion(s string) error {
	if !config.ValidRegion(s) {
		return fmt.Errorf("invalid AWS region %q", s)
	}
	return nil
}

func validBucket(s string) error {
	if !config.ValidBucket(s) {
		return fmt.Errorf("invalid bucket name %q", s)
	}
	return nil
}

func validTargetName(s string) error {
	if !config.ValidTargetName(s) {
		return errors.New("lower case letters, digits and -, up to 63, starting with a letter or digit")
	}
	return nil
}

func validPort(s string) error {
	if n, err := strconv.Atoi(s); err != nil || n < 1 || n > 65535 {
		return errors.New("a port from 1 to 65535")
	}
	return nil
}

func (a *app) configAddTargetCmd() *cobra.Command {
	var f targetFlags
	cmd := &cobra.Command{
		Use:   "add-target <name>",
		Short: "Add (or with --replace change) a target from flags, without AWS calls",
		Long: `Add a target from flags, without AWS calls: --instance, --account-id and
--bucket and --sql-user are required for a new target. With --replace an
existing target changes only in the fields given.

The file is written atomically with mode 0600. Comments are not kept: the
previous version stays next to it as .bak.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, set := args[0], cmd.Flags().Changed
			path, err := a.configFile()
			if err != nil {
				return err
			}
			cfg, err := config.LoadOrEmpty(path)
			if err != nil {
				return err
			}
			_, exists := cfg.Targets[name]
			if exists && !f.replace {
				return fmt.Errorf("%w: %s (use --replace to change it)", config.ErrTargetExists, name)
			}
			if !exists {
				var missing []string
				for _, flag := range []string{"instance", "account-id", "bucket", "sql-user"} {
					if !set(flag) {
						missing = append(missing, "--"+flag)
					}
				}
				if len(missing) > 0 {
					return fmt.Errorf("a new target needs %s", strings.Join(missing, ", "))
				}
			}
			if set("password") {
				if err := config.SupportedPasswordSource(f.password); err != nil {
					return err
				}
			}
			profiles, err := a.discovery().Profiles()
			if err != nil {
				return fmt.Errorf("AWS profiles: %w", err)
			}
			return a.saveTarget(cmd, path, name, f.replace, func(t config.Target) config.Target {
				if !exists {
					t.AWSProfile = a.defaultProfile()
					t.Connect.Mode = config.ModeTunnel
				}
				for flag, apply := range map[string]func(){
					"profile":    func() { t.AWSProfile = f.profile },
					"account-id": func() { t.AccountID = f.accountID },
					"instance":   func() { t.Instance = f.instance },
					"bucket":     func() { t.Bucket = f.bucket },
					"sql-user":   func() { t.SQL.User = f.sqlUser },
					"password":   func() { t.SQL.Password = f.password },
					"mode":       func() { t.Connect.Mode = f.mode },
					"local-port": func() { t.Connect.LocalPort = f.localPort },
					"protected":  func() { t.Protected = f.protected },
				} {
					if set(flag) {
						apply()
					}
				}
				if set("region") {
					t.Region = f.region
				} else if !exists {
					t.Region = profileRegion(profiles, t.AWSProfile)
				}
				if !exists && t.Connect.Mode == config.ModeExternal && !set("local-port") {
					t.Connect.LocalPort = defaultLocalPort
				}
				return t
			})
		},
	}
	f.register(cmd, true)
	return cmd
}

func (a *app) configRemoveTargetCmd() *cobra.Command {
	var confirm string
	cmd := &cobra.Command{
		Use:   "remove-target <name>",
		Short: "Remove a target (retype its name, or --confirm <name>)",
		Long: `Remove a target from the configuration. On a terminal it asks to retype the
name; otherwise --confirm must repeat it. Removing the last target removes
the file; the previous version stays next to it as .bak.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			path, err := a.configFile()
			if err != nil {
				return err
			}
			cfg, err := config.LoadOrEmpty(path)
			if err != nil {
				return err
			}
			t, ok := cfg.Targets[name]
			if !ok {
				return fmt.Errorf("%w %q (known: %s)", config.ErrUnknownTarget, name, strings.Join(cfg.Names(), ", "))
			}
			if !cmd.Flags().Changed("confirm") {
				if !stdinIsTerminal(a.stdin) {
					return fmt.Errorf("not a terminal: confirm with --confirm %s", name)
				}
				p := &prompter{in: bufio.NewReader(a.stdin), out: cmd.ErrOrStderr()}
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Removing target %s (instance %s, account %s).\n", name, t.Instance, t.AccountID)
				if confirm, err = p.ask("Type the target name to confirm", "", nil); err != nil {
					return err
				}
			}
			if confirm != name {
				return errors.New("the confirmation does not match the target name: nothing removed")
			}
			if err := config.Update(path, func(c *config.Config) error { return c.RemoveTarget(name) }); err != nil {
				return err
			}
			_, statErr := os.Stat(path)
			if a.output == "json" {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					Path        string `json:"path"`
					Target      string `json:"target"`
					FileRemoved bool   `json:"fileRemoved"`
				}{path, name, errors.Is(statErr, fs.ErrNotExist)})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Removed target %s from %s (previous version: %s.bak).\n", name, path, path)
			if errors.Is(statErr, fs.ErrNotExist) {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "No targets left: the file was removed. Add one with dialga config init.")
			}
			return err
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the target name again, to confirm without a terminal")
	return cmd
}

// maxAttempts bounds the questions asked again after an invalid answer.
const maxAttempts = 5

// prompter asks on the terminal: the question on stderr, the answer one line
// of stdin.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func (p *prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			return "", errors.New("no answer: the input ended")
		}
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// ask returns the answer, def for an empty one. check, when set, rejects an
// answer with the reason, and the question comes again.
func (p *prompter) ask(question, def string, check func(string) error) (string, error) {
	for range maxAttempts {
		if def != "" {
			_, _ = fmt.Fprintf(p.out, "%s [%s]: ", question, def)
		} else {
			_, _ = fmt.Fprintf(p.out, "%s: ", question)
		}
		answer, err := p.line()
		if err != nil {
			return "", err
		}
		if answer == "" {
			answer = def
		}
		if answer == "" {
			_, _ = fmt.Fprintln(p.out, "  an answer is needed")
			continue
		}
		if check != nil {
			if err := check(answer); err != nil {
				_, _ = fmt.Fprintf(p.out, "  %v\n", err)
				continue
			}
		}
		return answer, nil
	}
	return "", fmt.Errorf("%s: too many invalid answers", question)
}

func (p *prompter) confirm(question string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for range maxAttempts {
		_, _ = fmt.Fprintf(p.out, "%s [%s]: ", question, hint)
		answer, err := p.line()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		_, _ = fmt.Fprintln(p.out, "  answer y or n")
	}
	return false, fmt.Errorf("%s: too many invalid answers", question)
}

// choose lists rows numbered from 1 and returns the index picked by number
// or by key; def is the index of the default.
func (p *prompter) choose(question string, keys, rows []string, def int) (int, error) {
	for i, row := range rows {
		_, _ = fmt.Fprintf(p.out, "  %2d) %s\n", i+1, row)
	}
	answer, err := p.ask(question, keys[def], func(s string) error {
		if _, ok := pick(s, keys); !ok {
			return fmt.Errorf("a number from 1 to %d, or a name from the list", len(keys))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	i, _ := pick(answer, keys)
	return i, nil
}

func pick(answer string, keys []string) (int, bool) {
	if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(keys) {
		return n - 1, true
	}
	i := slices.Index(keys, answer)
	return i, i >= 0
}
