package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"

	"github.com/decisyon/dialga/engine/internal/awsx"
	"github.com/decisyon/dialga/engine/internal/config"
)

// EnvPassword is the variable that supplies the password of a "prompt" target.
const EnvPassword = "DIALGA_SQL_PASSWORD"

// ErrNeeded means the password cannot be resolved without asking: the source
// is prompt (or keyring) and nothing was found. The caller asks the user and
// hands the answer to Passwords.Set.
var ErrNeeded = errors.New("SQL password needed")

// ErrFixedSource means the password comes from env:VAR or secretsmanager:
// it cannot be typed or remembered.
var ErrFixedSource = errors.New("the password does not come from the user")

// ErrNoKeyring means no system keyring is usable on this machine.
var ErrNoKeyring = errors.New("no system keyring is usable")

// SecretsClient is the part of the Secrets Manager client Passwords uses.
type SecretsClient interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput,
		opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// keyringCacheTTL is how long a "is there an entry" answer is reused: the
// GUI asks for it on every refresh of the target list, and each lookup is a
// round trip to the keyring service.
const keyringCacheTTL = 15 * time.Second

// Passwords resolves the SQL password of a target, for the CLI, the API and
// the jobs. Resolution order: the memory of this process (every source but
// env and secretsmanager), then the source itself:
//
//	env:VAR              the variable VAR
//	prompt               DIALGA_SQL_PASSWORD, then the keyring entry
//	keyring              the keyring entry
//	secretsmanager:<arn> GetSecretValue with the target's profile and region
//
// A password lives in memory, and in the keyring when remembered: never in a
// file, a journal, an event or an error text. Safe for concurrent use.
type Passwords struct {
	// Getenv reads the environment (default: none set).
	Getenv func(string) string
	// Keyring is the system keyring (default: SystemKeyring()).
	Keyring Keyring
	// Secrets builds the Secrets Manager client of an AWS configuration
	// (default: the real one). Tests replace it.
	Secrets func(aws.Config) SecretsClient
	// LoadAWS loads the AWS configuration of a target, with the account
	// guard (default: awsx.Load). Tests replace it.
	LoadAWS func(ctx context.Context, t config.Target) (aws.Config, error)

	mu     sync.Mutex
	mem    map[string]memEntry
	cached map[string]presence
}

type memEntry struct {
	password string
	identity string // what the password was typed for
}

type presence struct {
	present bool
	at      time.Time
}

// NewPasswords is a resolver reading the environment through getenv, with
// the system keyring.
func NewPasswords(getenv func(string) string) *Passwords {
	return &Passwords{Getenv: getenv}
}

func (p *Passwords) env(key string) string {
	if p.Getenv == nil {
		return ""
	}
	return p.Getenv(key)
}

func (p *Passwords) keyring() Keyring {
	if p.Keyring == nil {
		p.Keyring = SystemKeyring()
	}
	return p.Keyring
}

// identity is what a password in memory belongs to: when it changes, the
// password typed for the old one must not be sent to the new one.
func identity(t config.Target) string {
	return strings.Join([]string{t.Instance, t.AccountID, t.Region, t.SQL.User}, "\x00")
}

// Resolve returns the password of target name, or ErrNeeded. Any other
// error says why the source could not be read (an empty variable, AWS).
func (p *Passwords) Resolve(ctx context.Context, name string, t config.Target) (string, error) {
	src, err := t.SQL.PasswordSource()
	if err != nil {
		return "", err
	}
	switch src.Kind {
	case config.PasswordEnv:
		if v := p.env(src.Ref); v != "" {
			return v, nil
		}
		return "", fmt.Errorf("environment variable %s is empty", src.Ref)
	case config.PasswordSecretsManager:
		return p.fromSecret(ctx, t, src.Ref)
	}
	if v, ok := p.memory(name, t); ok {
		return v, nil
	}
	if src.Kind == config.PasswordPrompt {
		if v := p.env(EnvPassword); v != "" {
			return v, nil
		}
	}
	if v, ok := p.fromKeyring(name); ok {
		return v, nil
	}
	return "", ErrNeeded
}

func (p *Passwords) memory(name string, t config.Target) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.mem[name]
	if ok && e.identity != identity(t) {
		delete(p.mem, name)
		return "", false
	}
	return e.password, ok
}

func (p *Passwords) fromKeyring(name string) (string, bool) {
	v, err := p.keyring().Get(name)
	p.mu.Lock()
	p.remember(name, err == nil && v != "")
	p.mu.Unlock()
	return v, err == nil && v != ""
}

// remember caches whether the keyring has an entry; p.mu is held.
func (p *Passwords) remember(name string, present bool) {
	if p.cached == nil {
		p.cached = map[string]presence{}
	}
	p.cached[name] = presence{present, time.Now()}
}

// fromSecret reads a Secrets Manager secret: JSON with a "password" field
// (the RDS-managed ones) or a plain string.
func (p *Passwords) fromSecret(ctx context.Context, t config.Target, arn string) (string, error) {
	load := p.LoadAWS
	if load == nil {
		load = func(ctx context.Context, t config.Target) (aws.Config, error) {
			cfg, _, err := awsx.Load(ctx, t.AWSProfile, t.Region, t.AccountID)
			return cfg, err
		}
	}
	cfg, err := load(ctx, t)
	if err != nil {
		return "", fmt.Errorf("secret %s: %w", arn, err)
	}
	var client SecretsClient
	if p.Secrets != nil {
		client = p.Secrets(cfg)
	} else {
		client = secretsmanager.NewFromConfig(cfg)
	}
	out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(arn)})
	if err != nil {
		return "", fmt.Errorf("secret %s: %w", arn, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("secret %s has no SecretString (binary secrets are not supported)", arn)
	}
	return passwordFromSecret(arn, *out.SecretString)
}

func passwordFromSecret(arn, s string) (string, error) {
	var doc map[string]any
	if strings.HasPrefix(strings.TrimSpace(s), "{") && json.Unmarshal([]byte(s), &doc) == nil {
		pw, _ := doc["password"].(string)
		if pw == "" {
			return "", fmt.Errorf("secret %s is JSON without a password field", arn)
		}
		return pw, nil
	}
	if s == "" {
		return "", fmt.Errorf("secret %s is empty", arn)
	}
	return s, nil
}

// PasswordStatus says where a target's password comes from and whether it
// can be resolved right now.
type PasswordStatus struct {
	Source      string // prompt, env, keyring or secretsmanager
	Available   bool   // resolvable without asking
	CanRemember bool   // a system keyring is usable
}

// Status never returns the password. secretsmanager counts as available: it
// is read when used.
func (p *Passwords) Status(name string, t config.Target) PasswordStatus {
	src, err := t.SQL.PasswordSource()
	st := PasswordStatus{Source: src.Kind}
	if err != nil {
		st.Source = config.PasswordPrompt
	}
	st.CanRemember = p.keyring().Usable()
	switch src.Kind {
	case config.PasswordEnv:
		st.Available = p.env(src.Ref) != ""
	case config.PasswordSecretsManager:
		st.Available = true
	default:
		_, mem := p.memory(name, t)
		st.Available = mem || (src.Kind == config.PasswordPrompt && p.env(EnvPassword) != "") ||
			(st.CanRemember && p.HasRemembered(name))
	}
	return st
}

// HasRemembered reports whether the keyring holds an entry for name (a
// recent answer may be reused).
func (p *Passwords) HasRemembered(name string) bool {
	p.mu.Lock()
	c, ok := p.cached[name]
	p.mu.Unlock()
	if ok && time.Since(c.at) < keyringCacheTTL {
		return c.present
	}
	_, present := p.fromKeyring(name)
	return present
}

// Set keeps the password of target name in memory and, with remember, in the
// keyring (ErrNoKeyring when there is none: nothing is stored). Targets
// whose password comes from env or secretsmanager answer ErrFixedSource.
func (p *Passwords) Set(name string, t config.Target, password string, remember bool) error {
	if err := p.checkSettable(t); err != nil {
		return err
	}
	if remember {
		kr := p.keyring()
		if !kr.Usable() {
			return ErrNoKeyring
		}
		if err := kr.Set(name, password); err != nil {
			return fmt.Errorf("system keyring: %w", err)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.mem == nil {
		p.mem = map[string]memEntry{}
	}
	p.mem[name] = memEntry{password, identity(t)}
	if remember {
		p.remember(name, true)
	}
	return nil
}

// checkSettable returns ErrFixedSource, with where the password comes from.
func (p *Passwords) checkSettable(t config.Target) error {
	src, err := t.SQL.PasswordSource()
	if err != nil {
		return err
	}
	switch src.Kind {
	case config.PasswordEnv:
		return fmt.Errorf("%w: it is read from the environment variable %s", ErrFixedSource, src.Ref)
	case config.PasswordSecretsManager:
		return fmt.Errorf("%w: it is read from AWS Secrets Manager", ErrFixedSource)
	}
	return nil
}

// CheckSettable reports whether Set accepts a password for t.
func (p *Passwords) CheckSettable(t config.Target) error { return p.checkSettable(t) }

// Forget drops the password of name from memory and removes its keyring
// entry; no error when there is none.
func (p *Passwords) Forget(name string) error {
	p.mu.Lock()
	delete(p.mem, name)
	delete(p.cached, name)
	p.mu.Unlock()
	kr := p.keyring()
	if !kr.Usable() {
		return nil
	}
	if err := kr.Delete(name); err != nil && !errors.Is(err, ErrKeyNotFound) {
		return fmt.Errorf("system keyring: %w", err)
	}
	return nil
}

// ForgetMemory drops the password of name from memory only, as when its
// target is removed.
func (p *Passwords) ForgetMemory(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.mem, name)
}

// Prune drops the memory of the targets that are not in names any more.
func (p *Passwords) Prune(names []string) {
	keep := make(map[string]bool, len(names))
	for _, n := range names {
		keep[n] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for n := range p.mem {
		if !keep[n] {
			delete(p.mem, n)
		}
	}
}
