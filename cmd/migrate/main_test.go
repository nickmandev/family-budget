package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"family-budget/internal/config"
)

func lookupFrom(values map[string]string) config.LookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		config.EnvDatabaseHost:     "db.example",
		config.EnvDatabasePort:     "5432",
		config.EnvDatabaseUser:     "budget",
		config.EnvDatabasePassword: "supersecret",
		config.EnvDatabaseName:     "family",
		config.EnvDatabaseSSLMode:  "disable",
	}
}

// TestRunUsesSharedDSNHelper proves the migrate path reuses the single Go DSN
// construction helper instead of hand-building a URL (there is no shell parity
// gap to test).
func TestRunUsesSharedDSNHelper(t *testing.T) {
	environment := validEnvironment()
	environment[config.EnvDatabaseUser] = "us@er:name"
	environment[config.EnvDatabasePassword] = "p@ss:w/rd?%&# "

	cfg, err := config.LoadFrom(lookupFrom(environment))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	want, err := cfg.Database.DSN()
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}

	var gotDSN, gotDir, gotCommand string
	var gotArgs []string
	err = run(context.Background(), []string{"-dir", "/migrations", "up-to", "0002"}, lookupFrom(environment), io.Discard, io.Discard,
		func(_ context.Context, dsn, dir, command string, args []string) error {
			gotDSN, gotDir, gotCommand, gotArgs = dsn, dir, command, args
			return nil
		})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotDSN != want {
		t.Fatalf("migrate DSN = %q, want the config helper output %q", gotDSN, want)
	}
	if gotDir != "/migrations" || gotCommand != "up-to" || len(gotArgs) != 1 || gotArgs[0] != "0002" {
		t.Fatalf("migrate args = dir %q command %q args %v", gotDir, gotCommand, gotArgs)
	}
}

func TestRunDefaultsToUp(t *testing.T) {
	var gotCommand string
	err := run(context.Background(), nil, lookupFrom(validEnvironment()), io.Discard, io.Discard,
		func(_ context.Context, _, _, command string, _ []string) error {
			gotCommand = command
			return nil
		})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotCommand != "up" {
		t.Fatalf("command = %q, want up", gotCommand)
	}
}

func TestRunRejectsMissingComponents(t *testing.T) {
	environment := validEnvironment()
	delete(environment, config.EnvDatabaseHost)

	called := false
	err := run(context.Background(), nil, lookupFrom(environment), io.Discard, io.Discard,
		func(context.Context, string, string, string, []string) error {
			called = true
			return nil
		})
	if err == nil || !strings.Contains(err.Error(), config.EnvDatabaseHost) {
		t.Fatalf("error = %v, want %s validation failure", err, config.EnvDatabaseHost)
	}
	if called {
		t.Fatal("migration ran despite invalid configuration")
	}
}

func TestRunRedactsPasswordOnFailure(t *testing.T) {
	cfg, err := config.LoadFrom(lookupFrom(validEnvironment()))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	dsn, err := cfg.Database.DSN()
	if err != nil {
		t.Fatalf("DSN: %v", err)
	}

	err = run(context.Background(), nil, lookupFrom(validEnvironment()), io.Discard, io.Discard,
		func(context.Context, string, string, string, []string) error {
			return errors.New("dial failed: " + dsn + " password=supersecret")
		})
	if err == nil {
		t.Fatal("expected a migration failure")
	}
	for _, secret := range []string{"supersecret", dsn} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("migration error leaked %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "db.example:5432/family") {
		t.Errorf("migration error %q does not name the database target", err)
	}
}

func TestHelpDoesNotContainPassword(t *testing.T) {
	var help strings.Builder
	err := run(context.Background(), []string{"-h"}, lookupFrom(validEnvironment()), io.Discard, &help,
		func(context.Context, string, string, string, []string) error { return nil })
	if err == nil {
		t.Fatal("expected flag.ErrHelp")
	}
	if strings.Contains(help.String(), "supersecret") {
		t.Fatalf("help output leaked the password: %s", help.String())
	}
}
