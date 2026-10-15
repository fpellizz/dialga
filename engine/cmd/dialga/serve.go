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

	"github.com/decisyon/dialga/engine/internal/api"
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
		Log:            log,
	}, stdout)
	if err != nil {
		log.Error("serve", "err", err)
		return 1
	}
	log.Info("serve stopped")
	return 0
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
