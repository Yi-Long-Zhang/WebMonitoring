package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"webmonitoring/internal/migrate"
	"webmonitoring/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		log.Fatal("POSTGRES_DSN is required")
	}
	// Which build applied the schema is part of the migration record: the
	// ledger should be answerable from the log of the run that changed it.
	// Importing the package is also what makes the image's
	// -X webmonitoring/internal/version.* build args effective; without it they
	// are silently inert.
	log.Printf("migrate %s (%s) built %s", version.Version, version.Commit, version.BuildTime)
	ctx := context.Background()
	runner, err := migrate.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer runner.Close()

	switch os.Args[1] {
	case "up":
		// Acknowledged destructive upgrades: applying a migration marked
		// "-- migrate:destructive" to a database that already holds data
		// requires this variable for the upgrade window only.
		allowDestructive := os.Getenv("MIGRATE_CONFIRM_DESTRUCTIVE") == "1" ||
			strings.EqualFold(os.Getenv("MIGRATE_CONFIRM_DESTRUCTIVE"), "true")
		if err := runner.Up(ctx, allowDestructive); err != nil {
			log.Fatal(err)
		}
	case "status":
		statuses, err := runner.Status(ctx)
		if err != nil {
			log.Fatal(err)
		}
		for _, status := range statuses {
			applied := "pending"
			if status.AppliedAt != nil {
				applied = "applied@" + status.AppliedAt.UTC().Format("2006-01-02T15:04:05Z")
			}
			fmt.Printf("%04d %-40s %s\n", status.Version, status.Name, applied)
		}
	case "baseline":
		flags := flag.NewFlagSet("baseline", flag.ExitOnError)
		version := flags.Int("version", 0, "last migration version already verified in the database")
		_ = flags.Parse(os.Args[2:]) //nolint:errcheck // 参数解析失败由后续的校验发现，这里不吞掉判断
		if *version <= 0 {
			log.Fatal("baseline requires --version")
		}
		if err := runner.Baseline(ctx, *version); err != nil {
			log.Fatal(err)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: migrate <up|status|baseline --version N>")
	fmt.Fprintln(os.Stderr, "POSTGRES_DSN must be set")
	fmt.Fprintln(os.Stderr, "up applies a destructive-marked migration to an existing database only when")
	fmt.Fprintln(os.Stderr, "MIGRATE_CONFIRM_DESTRUCTIVE=1 (or true) is set for the upgrade window")
}
