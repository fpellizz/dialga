package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/zalando/go-keyring"

	"github.com/decisyon/dialga/engine/internal/config"
)

// fakeKeyring is an in-memory Keyring.
type fakeKeyring struct {
	entries map[string]string
	usable  bool
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{entries: map[string]string{}, usable: true} }

func (k *fakeKeyring) Get(n string) (string, error) {
	if v, ok := k.entries[n]; ok {
		return v, nil
	}
	return "", ErrKeyNotFound
}
func (k *fakeKeyring) Set(n, v string) error { k.entries[n] = v; return nil }
func (k *fakeKeyring) Delete(n string) error {
	if _, ok := k.entries[n]; !ok {
		return ErrKeyNotFound
	}
	delete(k.entries, n)
	return nil
}
func (k *fakeKeyring) Usable() bool { return k.usable }

type fakeSecrets struct {
	value *string
	err   error
	gotID string
}

func (f *fakeSecrets) GetSecretValue(_ context.Context, in *secretsmanager.GetSecretValueInput,
	_ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.gotID = aws.ToString(in.SecretId)
	return &secretsmanager.GetSecretValueOutput{SecretString: f.value}, f.err
}

const testARN = "arn:aws:secretsmanager:us-east-1:123456789012:secret:rds-abc123"

func target(pw string) config.Target {
	return config.Target{Instance: "db-1", AccountID: "123456789012", Region: "us-east-1",
		SQL: config.SQL{User: "admin", Password: pw}}
}

func newPasswords(env map[string]string, kr Keyring) *Passwords {
	return &Passwords{Getenv: func(k string) string { return env[k] }, Keyring: kr}
}

func TestResolveOrder(t *testing.T) {
	ctx := context.Background()
	env := map[string]string{"MY_PW": "from-var", EnvPassword: "from-dialga"}
	kr := newFakeKeyring()
	p := newPasswords(env, kr)

	if got, err := p.Resolve(ctx, "a", target("env:MY_PW")); got != "from-var" || err != nil {
		t.Fatalf("env: %q, %v", got, err)
	}
	if _, err := p.Resolve(ctx, "a", target("env:OTHER")); err == nil || errors.Is(err, ErrNeeded) {
		t.Fatalf("empty env var must be a plain error, got %v", err)
	}
	if got, _ := p.Resolve(ctx, "a", target("prompt")); got != "from-dialga" {
		t.Fatalf("prompt without keyring: %q", got)
	}
	kr.entries["a"] = "from-keyring"
	if got, _ := p.Resolve(ctx, "a", target("prompt")); got != "from-dialga" {
		t.Fatalf("DIALGA_SQL_PASSWORD beats the keyring: %q", got)
	}
	delete(env, EnvPassword)
	if got, _ := p.Resolve(ctx, "a", target("prompt")); got != "from-keyring" {
		t.Fatalf("prompt with keyring: %q", got)
	}
	if got, _ := p.Resolve(ctx, "a", target("keyring")); got != "from-keyring" {
		t.Fatalf("keyring: %q", got)
	}
	if _, err := p.Resolve(ctx, "b", target("keyring")); !errors.Is(err, ErrNeeded) {
		t.Fatalf("keyring without entry: %v", err)
	}
	if _, err := p.Resolve(ctx, "b", target("prompt")); !errors.Is(err, ErrNeeded) {
		t.Fatalf("prompt without anything: %v", err)
	}
}

func TestMemoryWinsAndFollowsIdentity(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	kr.entries["a"] = "from-keyring"
	p := newPasswords(map[string]string{EnvPassword: "from-dialga"}, kr)
	tg := target("prompt")
	if err := p.Set("a", tg, "typed", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.Resolve(ctx, "a", tg); got != "typed" {
		t.Fatalf("memory first: %q", got)
	}
	if kr.entries["a"] != "from-keyring" {
		t.Fatal("remember=false must not touch the keyring")
	}
	for _, change := range []func(*config.Target){
		func(t *config.Target) { t.Instance = "db-2" },
		func(t *config.Target) { t.AccountID = "999999999999" },
		func(t *config.Target) { t.Region = "eu-west-1" },
		func(t *config.Target) { t.SQL.User = "other" },
	} {
		p.Set("a", tg, "typed", false) //nolint:errcheck // set above
		moved := tg
		change(&moved)
		if got, _ := p.Resolve(ctx, "a", moved); got == "typed" {
			t.Fatalf("password typed for another identity was reused: %+v", moved)
		}
	}
}

func TestSetRememberAndForget(t *testing.T) {
	ctx := context.Background()
	kr := newFakeKeyring()
	p := newPasswords(nil, kr)
	tg := target("prompt")
	if err := p.Set("a", tg, "pw", true); err != nil || kr.entries["a"] != "pw" {
		t.Fatalf("remember: %v %v", err, kr.entries)
	}
	if st := p.Status("a", tg); !st.Available || !st.CanRemember || st.Source != "prompt" {
		t.Fatalf("status %+v", st)
	}
	if err := p.Forget("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(ctx, "a", tg); !errors.Is(err, ErrNeeded) {
		t.Fatalf("after forget: %v", err)
	}
	if err := p.Forget("a"); err != nil {
		t.Fatalf("forget of nothing: %v", err)
	}
	if st := p.Status("a", tg); st.Available {
		t.Fatalf("status after forget %+v", st)
	}
}

func TestSetWithoutKeyringStoresNothing(t *testing.T) {
	kr := newFakeKeyring()
	kr.usable = false
	p := newPasswords(nil, kr)
	tg := target("prompt")
	if err := p.Set("a", tg, "pw", true); !errors.Is(err, ErrNoKeyring) {
		t.Fatalf("got %v", err)
	}
	if _, err := p.Resolve(context.Background(), "a", tg); !errors.Is(err, ErrNeeded) {
		t.Fatalf("nothing may be kept in memory: %v", err)
	}
	if err := p.Set("a", tg, "pw", false); err != nil {
		t.Fatalf("memory only works without a keyring: %v", err)
	}
	if st := p.Status("a", tg); st.CanRemember || !st.Available {
		t.Fatalf("status %+v", st)
	}
	if err := p.Forget("a"); err != nil {
		t.Fatalf("forget without keyring: %v", err)
	}
}

func TestSetRefusesFixedSources(t *testing.T) {
	p := newPasswords(nil, newFakeKeyring())
	for _, ref := range []string{"env:MY_PW", "secretsmanager:" + testARN} {
		err := p.Set("a", target(ref), "pw", false)
		if !errors.Is(err, ErrFixedSource) {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	if err := p.Set("a", target("env:MY_PW"), "pw", false); !strings.Contains(err.Error(), "MY_PW") {
		t.Fatalf("the text must say where it comes from: %v", err)
	}
}

func TestSecretsManager(t *testing.T) {
	tests := []struct {
		name   string
		secret *string
		err    error
		want   string
		fail   string
	}{
		{"rds json", aws.String(`{"username":"admin","password":"s3cret"}`), nil, "s3cret", ""},
		{"plain", aws.String("plain-pw"), nil, "plain-pw", ""},
		{"json without password", aws.String(`{"username":"admin"}`), nil, "", "without a password field"},
		{"binary", nil, nil, "", "no SecretString"},
		{"empty", aws.String(""), nil, "", "is empty"},
		{"denied", nil, errors.New("AccessDenied"), "", "AccessDenied"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeSecrets{value: tc.secret, err: tc.err}
			p := newPasswords(nil, newFakeKeyring())
			p.LoadAWS = func(context.Context, config.Target) (aws.Config, error) { return aws.Config{}, nil }
			p.Secrets = func(aws.Config) SecretsClient { return fake }
			got, err := p.Resolve(context.Background(), "a", target("secretsmanager:"+testARN))
			if tc.fail == "" {
				if err != nil || got != tc.want {
					t.Fatalf("got %q, %v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.fail) {
				t.Fatalf("got %q, %v; want error with %q", got, err, tc.fail)
			}
			if fake.gotID != testARN {
				t.Fatalf("secret id %q", fake.gotID)
			}
		})
	}
	p := newPasswords(nil, newFakeKeyring())
	p.LoadAWS = func(context.Context, config.Target) (aws.Config, error) {
		return aws.Config{}, errors.New("wrong account")
	}
	if _, err := p.Resolve(context.Background(), "a", target("secretsmanager:"+testARN)); err == nil ||
		!strings.Contains(err.Error(), "wrong account") {
		t.Fatalf("the AWS load error must surface: %v", err)
	}
	if st := p.Status("a", target("secretsmanager:"+testARN)); !st.Available || st.Source != "secretsmanager" {
		t.Fatalf("status %+v", st)
	}
}

func TestErrorsNeverCarryThePassword(t *testing.T) {
	p := newPasswords(nil, newFakeKeyring())
	p.LoadAWS = func(context.Context, config.Target) (aws.Config, error) { return aws.Config{}, nil }
	p.Secrets = func(aws.Config) SecretsClient {
		return &fakeSecrets{value: aws.String(`{"username":"admin"}`)}
	}
	_, err := p.Resolve(context.Background(), "a", target("secretsmanager:"+testARN))
	if err == nil || strings.Contains(err.Error(), "admin") {
		t.Fatalf("error text leaks the secret: %v", err)
	}
}

func TestPrune(t *testing.T) {
	p := newPasswords(nil, newFakeKeyring())
	tg := target("prompt")
	_ = p.Set("a", tg, "pw", false)
	_ = p.Set("b", tg, "pw", false)
	p.Prune([]string{"a"})
	if _, ok := p.memory("a", tg); !ok {
		t.Fatal("a pruned")
	}
	if _, ok := p.memory("b", tg); ok {
		t.Fatal("b kept")
	}
}

func TestStatusEnv(t *testing.T) {
	p := newPasswords(map[string]string{"MY_PW": "x"}, newFakeKeyring())
	if st := p.Status("a", target("env:MY_PW")); !st.Available || st.Source != "env" {
		t.Fatalf("%+v", st)
	}
	if st := p.Status("a", target("env:OTHER")); st.Available {
		t.Fatalf("%+v", st)
	}
	if st := p.Status("a", target("keyring")); st.Available || st.Source != "keyring" {
		t.Fatalf("%+v", st)
	}
}

func TestSystemKeyring(t *testing.T) {
	keyring.MockInit()
	k := SystemKeyring()
	if !k.Usable() {
		t.Fatal("mock keyring must be usable")
	}
	if _, err := k.Get("a"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	if err := k.Set("a", "pw"); err != nil {
		t.Fatal(err)
	}
	if v, err := k.Get("a"); v != "pw" || err != nil {
		t.Fatalf("get: %q %v", v, err)
	}
	if err := k.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if err := k.Delete("a"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	keyring.MockInitWithError(errors.New("no service"))
	if SystemKeyring().Usable() {
		t.Fatal("a failing keyring must not be usable")
	}
}
