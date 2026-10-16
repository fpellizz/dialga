package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/decisyon/dialga/engine/internal/api"
	"github.com/decisyon/dialga/engine/internal/jobs"
)

// devOrigin is the Vite dev server, allowed only with DIALGA_DEV=1.
const devOrigin = "http://localhost:1420"

// serve runs the local API for the desktop shell (Tauri sidecar).
//
// Protocol with the shell:
//  1. the shell writes the session token as the first line on stdin, so it
//     never shows up in the process list;
//  2. the engine answers with one JSON line on stdout:
//     {"event":"ready","port":N,"version":"…"};
//  3. the engine exits when stdin closes (the shell is gone) or on
//     SIGINT/SIGTERM.
//
// The jobs are kept in the journal (jobs.DefaultDir): when the engine stops,
// the running ones are interrupted and written there before it exits.
//
// The targets come from configPath, read on every request. SQL passwords
// follow each target's sql.password (env:VAR, or DIALGA_SQL_PASSWORD for
// "prompt"): the engine inherits them from the desktop app's environment.
//
// Environment:
//
//	DIALGA_DEV=1  also allow the Vite dev server origin
func serve(stdin io.Reader, stdout, stderr io.Writer, configPath string) int {
	log := slog.New(slog.NewTextHandler(stderr, nil))
	reader := bufio.NewReader(stdin)
	token, err := readToken(reader)
	if err != nil {
		log.Error("serve", "err", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// Watchdog: the shell holds our stdin; EOF means it is gone.
		_, _ = io.Copy(io.Discard, reader)
		cancel()
	}()

	// Stop the jobs as soon as the engine stops: that also ends their event
	// streams, so the HTTP shutdown does not wait for them.
	manager := openJobs(log)
	jobsClosed := make(chan struct{})
	go func() {
		defer close(jobsClosed)
		<-ctx.Done()
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelClose()
		if err := manager.Close(closeCtx); err != nil {
			log.Warn("jobs", "err", err)
		}
	}()
	defer func() {
		cancel()
		<-jobsClosed
	}()

	origins := slices.Clone(api.DefaultOrigins)
	if os.Getenv("DIALGA_DEV") == "1" {
		origins = append(origins, devOrigin)
	}
	err = api.Serve(ctx, api.Options{
		Token:          token,
		AllowedOrigins: origins,
		Version:        version,
		ConfigPath:     configPath,
		Getenv:         os.Getenv,
		Jobs:           manager,
		Log:            log,
	}, stdout)
	if err != nil {
		log.Error("serve", "err", err)
		return 1
	}
	log.Info("serve stopped")
	return 0
}

// openJobs opens the job journal. Without it (another engine holds it, no
// state directory) the jobs of this session live in memory only.
func openJobs(log *slog.Logger) *jobs.Manager {
	dir, err := jobs.DefaultDir()
	if err == nil {
		var m *jobs.Manager
		if m, err = jobs.Open(dir, jobs.Options{Log: log}); err == nil {
			return m
		}
	}
	log.Warn("job journal unavailable: jobs are kept in memory only", "err", err)
	return jobs.New(jobs.Options{Log: log})
}

func readToken(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("no session token on stdin: %w", err)
	}
	token := strings.TrimSpace(line)
	if len(token) < 32 {
		return "", errors.New("session token too short (32 characters at least)")
	}
	return token, nil
}
