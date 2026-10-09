package store

import (
	"context"
	"hash/fnv"
	"sync"
)

// ReplicaLease serializes work that must run once across control-plane
// replicas, such as the retention sweep.
//
// Implementations must not block. A replica that finds the lease held skips the
// round instead of queueing behind it, because the holder is already doing the
// same work over the same rows.
type ReplicaLease interface {
	// TryAcquire takes the named lease. A nil or unacquired result means the
	// work is already in progress elsewhere and the caller must skip. When
	// acquired is true, release is non-nil and must be called exactly once; it
	// is safe to defer.
	TryAcquire(ctx context.Context, name string) (release func(), acquired bool, err error)
}

// leaseKey maps a lease name to the bigint pg_advisory_lock takes. FNV-1a keeps
// the names readable at the call site instead of scattering magic numbers; a
// collision between two names would only over-serialize them.
func leaseKey(name string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(name))
	return int64(hash.Sum64()) //nolint:gosec // 咨询锁只需要一个稳定的键；符号位无意义，溢出也不是此处的语义
}

// TryAcquire implements ReplicaLease with a session-level PostgreSQL advisory
// lock. The connection is held for the whole lease because an advisory lock is
// released together with the session that took it.
func (p *PostgresRepository) TryAcquire(ctx context.Context, name string) (func(), bool, error) {
	key := leaseKey(name)
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}
	// The release is idempotent: callers defer it, and a caller that also
	// releases explicitly must not return the connection to the pool twice.
	var once sync.Once
	return func() {
		once.Do(func() {
			// A cancelled sweep still has to free the lease, so the unlock runs
			// on a background context. Losing the session would drop it anyway.
			_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, key) //nolint:errcheck // 尽力而为的簿记/解锁：失败不影响本次结果
			conn.Release()
		})
	}, true, nil
}

var _ ReplicaLease = (*PostgresRepository)(nil)
