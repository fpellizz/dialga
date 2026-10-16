package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// fakePluginEnv makes the test binary act as session-manager-plugin (see TestMain).
const fakePluginEnv = "DIALGA_TEST_FAKE_PLUGIN"

func TestMain(m *testing.M) {
	switch os.Getenv(fakePluginEnv) {
	case "serve":
		// Like the plugin: routine lines, then wait for SIGTERM.
		fmt.Println("Starting session with SessionId: s-1")
		if !strings.Contains(os.Getenv(os.Args[1]), `"SessionId":"s-1"`) {
			fmt.Println("no session in the environment")
			os.Exit(1)
		}
		fmt.Println("Waiting for connections...")
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM)
		fmt.Println("ready for SIGTERM")
		<-c
		fmt.Println("Exiting session with sessionId: s-1.")
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "An error occurred (TargetNotConnected) when calling the StartSession operation")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestPluginLauncher(t *testing.T) {
	l := pluginLauncher{path: os.Args[0]}
	t.Setenv(fakePluginEnv, "serve")
	p, err := l.Start([]string{responseEnv}, []string{responseEnv + `={"SessionId":"s-1"}`})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for p.Output() != "ready for SIGTERM" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	p.Stop()
	if err := p.Wait(); runtime.GOOS != "windows" && err != nil {
		t.Fatalf("Wait after Stop: %v (output %q)", err, p.Output())
	}
	if runtime.GOOS != "windows" && p.Output() != "Exiting session with sessionId: s-1." {
		t.Fatalf("Output() = %q", p.Output())
	}
	p.Stop() // no-op once exited

	t.Setenv(fakePluginEnv, "fail")
	p, err = l.Start(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Wait(); err == nil || !strings.Contains(p.Output(), "TargetNotConnected") {
		t.Fatalf("Wait() = %v, output %q", err, p.Output())
	}
}

func TestPluginArgs(t *testing.T) {
	token, stream := "token-"+strings.Repeat("x", 8), "wss://ssmmessages.us-east-1.amazonaws.com/v1/data-channel/s-1"
	out := &ssm.StartSessionOutput{SessionId: aws.String("s-1"), TokenValue: aws.String(token), StreamUrl: aws.String(stream)}
	in := &ssm.StartSessionInput{Target: aws.String("i-1"), DocumentName: aws.String(Document),
		Parameters: map[string][]string{"host": {"db.example.rds.amazonaws.com"}, "portNumber": {"1433"}, "localPortNumber": {"41433"}}}
	args, env, err := pluginArgs(out, "us-east-1", "sales", in, "https://ssm.us-east-1.amazonaws.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(args, " "), token) || strings.Contains(strings.Join(args, " "), stream) {
		t.Fatalf("the session leaked into the arguments: %q", args)
	}
	want := []string{responseEnv, "us-east-1", "StartSession", "sales"}
	if len(args) != 6 || !equal(args[:4], want) || args[5] != "https://ssm.us-east-1.amazonaws.com" {
		t.Fatalf("args %q", args)
	}
	var req struct {
		Target, DocumentName string
		Parameters           map[string][]string
	}
	if err := json.Unmarshal([]byte(args[4]), &req); err != nil || req.Target != "i-1" || req.DocumentName != Document ||
		req.Parameters["localPortNumber"][0] != "41433" || req.Parameters["host"][0] != "db.example.rds.amazonaws.com" {
		t.Fatalf("request %q: %+v, %v", args[4], req, err)
	}
	name, value, _ := strings.Cut(env[0], "=")
	var resp struct{ SessionId, TokenValue, StreamUrl string }
	if len(env) != 1 || name != responseEnv || json.Unmarshal([]byte(value), &resp) != nil ||
		resp.SessionId != "s-1" || resp.TokenValue != token || resp.StreamUrl != stream {
		t.Fatalf("env %q", env)
	}
}

func equal(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }

func TestReadsEnvResponse(t *testing.T) {
	for v, want := range map[string]bool{
		"1.2.835.0": true, "1.2.835.0\n": true, "1.2.497.1": true, "1.3": true, "2": true,
		"1.2.497.0": false, "1.2.463.0": false, "1.1.61.0": false, "": false, "abc": false, "1.2.x.0": false, "1.2.3.4.5": false,
	} {
		if got := readsEnvResponse(v); got != want {
			t.Errorf("readsEnvResponse(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestLookPlugin(t *testing.T) {
	notOnPath := func(string) (string, error) { return "", errors.New("not found") }
	known := filepath.Join(t.TempDir(), pluginName)
	if err := os.WriteFile(known, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	onPath := func(string) (string, error) { return "/opt/bin/" + pluginName, nil }
	if got, err := lookPlugin(onPath, []string{known}); err != nil || got != "/opt/bin/"+pluginName {
		t.Fatalf("on PATH: %q, %v", got, err)
	}
	if got, err := lookPlugin(notOnPath, []string{filepath.Join(t.TempDir(), "missing"), known}); err != nil || got != known {
		t.Fatalf("known path: %q, %v", got, err)
	}
	_, err := lookPlugin(notOnPath, []string{t.TempDir()})
	if !errors.Is(err, ErrPluginMissing) || !strings.Contains(err.Error(), "install it") {
		t.Fatalf("missing: %v", err)
	}
	if !strings.Contains(installHint("linux", "arm64"), "linux_arm64/session-manager-plugin.rpm") ||
		!strings.HasPrefix(installHint("darwin", "arm64"), "https://docs.aws.amazon.com/") {
		t.Fatalf("install hints: %q, %q", installHint("linux", "arm64"), installHint("darwin", "arm64"))
	}
}

func TestEndpointOf(t *testing.T) {
	ctx := context.Background()
	if got, err := endpointOf(ctx, ssm.Options{Region: "us-east-1"}); err != nil || got != "https://ssm.us-east-1.amazonaws.com" {
		t.Fatalf("default endpoint %q, %v", got, err)
	}
	if got, err := endpointOf(ctx, ssm.Options{Region: "us-east-1", BaseEndpoint: aws.String("https://vpce.example")}); err != nil || got != "https://vpce.example" {
		t.Fatalf("custom endpoint %q, %v", got, err)
	}
}

func TestLastLine(t *testing.T) {
	l := &lastLine{}
	for _, chunk := range []string{
		"Starting session with SessionId: s-1\nPort 41433 opened for sessionId s-1.\n",
		"Waiting for connections...\n\nConnection accepted for session [s-1]\n",
		"Cannot perform start session: dial tcp: lookup ssmmessages",
		": no such host\n",
		"debug: TokenValue in the response\nwss://ssmmessages.example/v1/data-channel/s-1?x\n",
	} {
		if _, err := l.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got := l.String(); got != "Cannot perform start session: dial tcp: lookup ssmmessages: no such host" {
		t.Fatalf("String() = %q", got)
	}
}
