// Command migrate is the one-shot schema runner. It builds the PostgreSQL DSN
// from the same connection components as the server, using config.Database.DSN
// so the migrate path and the application can never disagree about encoding or
// sslmode, then runs the pinned Goose migration set.
//
// Operators never supply a connection URL. The API still never migrates on
// startup; this binary is invoked by the compose `migrate` service or manually.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"family-budget/internal/config"
)

// migrateFunc runs one Goose command. It is a seam so the wiring and error
// redaction can be tested without a live database.
type migrateFunc func(ctx context.Context, dsn, dir, command string, args []string) error

func main() {
	if err := run(context.Background(), os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr, runGoose); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

// run validates configuration, constructs the DSN with the shared helper and
// executes the requested Goose command. Errors never include the DSN or its
// password.
func run(ctx context.Context, args []string, lookup config.LookupFunc, stdout, stderr io.Writer, migrate migrateFunc) error {
	if migrate == nil {
		return fmt.Errorf("migration runner is required")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", "migrations", "directory containing the Goose migration files")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: migrate [-dir DIR] [goose-command [args...]]")
		fmt.Fprintln(stderr, "The PostgreSQL connection is built from POSTGRES_HOST, POSTGRES_PORT,")
		fmt.Fprintln(stderr, "POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_DB and POSTGRES_SSLMODE.")
		fmt.Fprintln(stderr, "goose-command defaults to up.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.LoadFrom(lookup)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	dsn, err := cfg.Database.DSN()
	if err != nil {
		return fmt.Errorf("invalid database configuration: %w", err)
	}

	command := "up"
	var commandArgs []string
	if rest := flags.Args(); len(rest) > 0 {
		command = rest[0]
		commandArgs = rest[1:]
	}

	if err := migrate(ctx, dsn, *dir, command, commandArgs); err != nil {
		return fmt.Errorf("goose %s: %s", command, cfg.Database.RedactError(err, dsn))
	}
	fmt.Fprintf(stdout, "goose %s complete\n", command)
	return nil
}

// runGoose opens a pgx database handle and delegates to the pinned Goose
// library.
func runGoose(ctx context.Context, dsn, dir, command string, args []string) error {
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	return goose.RunContext(ctx, command, db, dir, args...)
}
