package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"webmonitoring/db/migrations"
)

const (
	lockKey       int64 = 0x574d5f4d49475241
	migrationsDir       = "schema_migrations"
)

type Runner struct {
	pool *pgxpool.Pool
}

type Status struct {
	Version   int
	Name      string
	AppliedAt *time.Time
	Checksum  string
}

func New(ctx context.Context, dsn string) (*Runner, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("configure migration database: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		if err := pool.Ping(ctx); err == nil {
			return &Runner{pool: pool}, nil
		} else {
			lastErr = err
		}
		delay := time.Duration(attempt+1) * 500 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			pool.Close()
			return nil, fmt.Errorf("connect migration database: %w", ctx.Err())
		case <-timer.C:
		}
	}
	pool.Close()
	return nil, fmt.Errorf("connect migration database after retries: %w", lastErr)
}

func (r *Runner) Close() {
	r.pool.Close()
}

func (r *Runner) Up(ctx context.Context) error {
	return r.withLock(ctx, func(ctx context.Context, conn *pgxpool.Conn) error {
		if err := r.ensureTable(ctx, conn); err != nil {
			return err
		}
		applied, err := r.applied(ctx, conn)
		if err != nil {
			return err
		}
		known := make(map[int]struct{}, len(migrationFiles()))
		for _, file := range migrationFiles() {
			known[file.version] = struct{}{}
		}
		for version := range applied {
			if _, ok := known[version]; !ok {
				return fmt.Errorf("database contains unknown migration version %d", version)
			}
		}
		for _, file := range migrationFiles() {
			checksum := checksum(file.contents)
			if current, ok := applied[file.version]; ok {
				if current.name != file.name || current.checksum != checksum {
					return fmt.Errorf("migration %s integrity mismatch: recorded_name=%s recorded_checksum=%s file_checksum=%s",
						file.name, current.name, current.checksum, checksum)
				}
				continue
			}
			if err := r.apply(ctx, conn, file, checksum); err != nil {
				return err
			}
		}
		return nil
	})
}

// Baseline records an already manually verified schema as applied. It never
// executes SQL and intentionally requires an explicit version from the caller.
func (r *Runner) Baseline(ctx context.Context, version int) error {
	return r.withLock(ctx, func(ctx context.Context, conn *pgxpool.Conn) error {
		if err := r.ensureTable(ctx, conn); err != nil {
			return err
		}
		files := migrationFiles()
		if version < 1 || version > files[len(files)-1].version {
			return fmt.Errorf("baseline version %d is not available", version)
		}
		applied, err := r.applied(ctx, conn)
		if err != nil {
			return err
		}
		if len(applied) != 0 {
			return errors.New("cannot baseline a database with recorded migrations")
		}
		if err := verifyExistingSchema(ctx, conn); err != nil {
			return err
		}
		tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return fmt.Errorf("begin baseline transaction: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		for _, file := range files {
			if file.version > version {
				break
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO schema_migrations (version, name, checksum, applied_at)
				VALUES ($1, $2, $3, now())`,
				file.version, file.name, checksum(file.contents)); err != nil {
				return fmt.Errorf("record baseline migration %s: %w", file.name, err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit baseline: %w", err)
		}
		return nil
	})
}

func (r *Runner) Status(ctx context.Context) ([]Status, error) {
	if err := r.ensureTable(ctx, r.pool); err != nil {
		return nil, err
	}
	applied, err := r.applied(ctx, r.pool)
	if err != nil {
		return nil, err
	}
	result := make([]Status, 0, len(migrationFiles()))
	for _, file := range migrationFiles() {
		item := Status{
			Version: file.version, Name: file.name, Checksum: checksum(file.contents),
		}
		if current, ok := applied[file.version]; ok {
			item.AppliedAt = &current.appliedAt
			item.Checksum = current.checksum
		}
		result = append(result, item)
	}
	return result, nil
}

type migrationFile struct {
	version  int
	name     string
	contents []byte
}

type appliedMigration struct {
	name      string
	checksum  string
	appliedAt time.Time
}

func migrationFiles() []migrationFile {
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		panic(fmt.Sprintf("read embedded migrations: %v", err))
	}
	result := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			panic(fmt.Sprintf("migration filename %q has no numeric prefix", entry.Name()))
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			panic(fmt.Sprintf("migration filename %q has invalid version: %v", entry.Name(), err))
		}
		contents, err := fs.ReadFile(migrations.Files, entry.Name())
		if err != nil {
			panic(fmt.Sprintf("read migration %q: %v", entry.Name(), err))
		}
		result = append(result, migrationFile{version: version, name: entry.Name(), contents: contents})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	for index := 1; index < len(result); index++ {
		if result[index-1].version == result[index].version {
			panic(fmt.Sprintf("duplicate migration version %d", result[index].version))
		}
	}
	return result
}

func verifyExistingSchema(ctx context.Context, conn *pgxpool.Conn) error {
	const query = `
		SELECT to_regclass('public.tenants'), to_regclass('public.users'),
			to_regclass('public.assets'), to_regclass('public.detection_jobs'),
			to_regclass('public.evidence'), to_regclass('public.incidents')`
	var tables [6]*string
	if err := conn.QueryRow(ctx, query).Scan(
		&tables[0], &tables[1], &tables[2], &tables[3], &tables[4], &tables[5],
	); err != nil {
		return fmt.Errorf("inspect existing schema for baseline: %w", err)
	}
	for _, table := range tables {
		if table == nil {
			return errors.New("cannot baseline: required application tables are missing")
		}
	}
	return nil
}

func checksum(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

type database interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (r *Runner) ensureTable(ctx context.Context, db database) error {
	_, err := db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version integer PRIMARY KEY,
			name text NOT NULL,
			checksum char(64) NOT NULL,
			applied_at timestamptz NOT NULL
		)`)
	if err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	return nil
}

func (r *Runner) applied(ctx context.Context, db database) (map[int]appliedMigration, error) {
	rows, err := db.Query(ctx, `SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read migration state: %w", err)
	}
	defer rows.Close()
	result := make(map[int]appliedMigration)
	for rows.Next() {
		var version int
		var item appliedMigration
		if err := rows.Scan(&version, &item.name, &item.checksum, &item.appliedAt); err != nil {
			return nil, fmt.Errorf("scan migration state: %w", err)
		}
		result[version] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration state: %w", err)
	}
	return result, nil
}

func (r *Runner) apply(ctx context.Context, conn *pgxpool.Conn, file migrationFile, fileChecksum string) error {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", file.name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, string(file.contents)); err != nil {
		return fmt.Errorf("execute migration %s: %w", file.name, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO schema_migrations (version, name, checksum, applied_at)
		VALUES ($1, $2, $3, now())`, file.version, file.name, fileChecksum); err != nil {
		return fmt.Errorf("record migration %s: %w", file.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", file.name, err)
	}
	return nil
}

func (r *Runner) withLock(ctx context.Context, action func(context.Context, *pgxpool.Conn) error) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lockKey)
	}()
	return action(ctx, conn)
}
