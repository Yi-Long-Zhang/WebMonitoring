// Command rewrap re-seals stored credentials under the newest key version.
//
// It is an operator action, not a server: a key rotation is planned, run once
// against a deployment, verified, and then the retired key is removed from
// ALERT_SECRET_KEYS. Running it in the API process would put a long, write-heavy
// pass behind a request path, and would make "who rotated the keys, and when"
// a question about a goroutine rather than about an audited command.
//
// Usage:
//
//	rewrap --dry-run     # report what would change; write nothing
//	rewrap               # perform the rewrite
//
// POSTGRES_DSN and the key material are read from the environment, the same two
// variables the API uses, so the command re-seals under exactly the key the API
// would write with.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"webmonitoring/internal/config"
	"webmonitoring/internal/rewrap"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
	"webmonitoring/internal/version"
)

// leaseName serializes the pass across processes. It is distinct from the
// retention lease on purpose: an operator running this while the control plane
// is up must not be made to wait on, or block, an unrelated maintenance sweep.
const leaseName = "security.rewrap"

func main() {
	dryRun := flag.Bool("dry-run", false, "report what would be rewrapped without writing anything")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// The build identity of the process that touched customer data, recorded
	// before it touches anything. Importing the package is what makes the
	// image's -X webmonitoring/internal/version.* build args effective; without
	// it they are silently inert.
	logger.Info("rewrap build",
		"version", version.Version, "commit", version.Commit, "build_time", version.BuildTime)
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		logger.Error("POSTGRES_DSN is required: rewrap reads the deployed rows, so it cannot run without a database")
		os.Exit(2)
	}
	material := config.SecretKeyMaterial(os.Getenv)
	if material == "" {
		logger.Error("ALERT_SECRET_KEY or ALERT_SECRET_KEYS is required")
		os.Exit(2)
	}
	keys, err := security.NewKeyring(material)
	if err != nil {
		logger.Error("invalid key material", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	repository, err := store.NewPostgresRepository(ctx, dsn, material, store.PoolConfigFromEnv(os.Getenv))
	if err != nil {
		logger.Error("connect repository", "error", err)
		os.Exit(1)
	}
	defer repository.Close()

	release, acquired, err := repository.TryAcquire(ctx, leaseName)
	if err != nil {
		logger.Error("take rewrap lease", "error", err)
		os.Exit(1)
	}
	if !acquired {
		// Not an error: the other process is doing the same work over the same
		// rows, and a second pass would only re-seal what the first just sealed.
		logger.Info("another rewrap is already running; nothing to do", "lease", leaseName)
		return
	}
	defer release()

	runner := rewrap.NewRunner(repository, keys, logger)
	stats, runErr := runner.Run(ctx, *dryRun)
	if auditErr := rewrap.RecordAudit(ctx, repository, keys.PrimaryVersion(), *dryRun, stats, runErr); auditErr != nil {
		// The rewrite itself is what matters; a missing audit row is reported
		// but must not turn a successful rotation into a failed command.
		logger.Warn("could not record the rewrap audit event", "error", auditErr)
	}
	logger.Info("rewrap finished",
		"dryRun", *dryRun,
		"primaryVersion", keys.PrimaryVersion(),
		"channelsScanned", stats.ChannelsScanned,
		"channelsRewrapped", stats.ChannelsRewrapped,
		"policiesScanned", stats.PoliciesScanned,
		"policiesRewrapped", stats.PoliciesRewrapped,
		"alreadyCurrent", stats.AlreadyCurrent,
		"failures", len(stats.Failures),
	)
	for _, failure := range stats.Failures {
		logger.Error("value could not be rewrapped", "detail", failure)
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, runErr)
		os.Exit(1)
	}
	if *dryRun && stats.Rewrapped() {
		fmt.Println("dry run: re-run without --dry-run to move these values to the current key")
	}
}
