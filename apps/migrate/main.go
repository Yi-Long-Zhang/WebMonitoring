package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"webmonitoring/internal/migrate"
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
	ctx := context.Background()
	runner, err := migrate.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer runner.Close()

	switch os.Args[1] {
	case "up":
		if err := runner.Up(ctx); err != nil {
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
		_ = flags.Parse(os.Args[2:])
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
}
