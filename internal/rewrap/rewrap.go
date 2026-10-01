// Package rewrap rewrites stored credentials under the newest key version.
//
// Keyring makes a rotation readable -- an old key stays configured so existing
// ciphertext still decrypts -- but readable is not the same as rotated. Without
// this pass, a deployment that raised ALERT_SECRET_KEYS from v1 to v2 keeps
// every value it ever stored sealed under v1 forever, and the retired key can
// never be dropped from the configuration. The rotation would have moved which
// key signs new values and nothing else.
//
// The rewrite cannot be a single SQL statement. Each ciphertext is bound to its
// row through associated data derived from the tenant and the row id (and, for
// channels, a field suffix), so the plaintext is only reachable with the same
// context the write used. A batch UPDATE has no way to reconstruct that per row,
// which is why this walks rows and re-seals each one.
package rewrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

// Stats reports what one pass saw and changed. The rewrapped counts are per
// field, not per row: a channel has two encrypted columns and a policy with
// login state has two more, and an operator checking the result of a rotation
// wants to know every value moved, not just every row touched.
type Stats struct {
	ChannelsScanned   int
	ChannelsRewrapped int
	PoliciesScanned   int
	PoliciesRewrapped int
	// AlreadyCurrent counts values that were sealed under the primary version
	// and so were left alone. Reported separately from the scanned totals because
	// a second run of this tool should be a no-op, and this number is how that is
	// confirmed.
	AlreadyCurrent int
	// Failures names rows that could not be read or written. A non-empty list is
	// returned alongside an error: the pass is resumable, so what succeeded is
	// worth keeping even when part of it did not.
	Failures []string
}

// Rewrapped reports whether the pass changed anything.
func (s Stats) Rewrapped() bool {
	return s.ChannelsRewrapped > 0 || s.PoliciesRewrapped > 0
}

// Enumerator is the slice of the repository this pass needs: every tenant, and
// per tenant the assets that can carry a policy and the channels that carry
// credentials.
//
// It is a narrow interface rather than the full store.Repository so the pass can
// be exercised against an in-memory fake, and so it is obvious from the type
// which reads this operation is allowed to perform.
type Enumerator interface {
	ListTenants(ctx context.Context, opts store.ListOptions) ([]domain.Tenant, string, error)
	ListAssets(ctx context.Context, tenantID string, opts store.ListOptions) ([]domain.Asset, string, error)
	ListAlertChannelsWithSecrets(ctx context.Context, tenantID string) ([]domain.AlertChannel, error)
	GetPolicy(ctx context.Context, tenantID, assetID string) (domain.MonitorPolicy, error)
}

// Target is what this pass reads from and writes back to. The two writes are
// narrow on purpose: they name exactly the columns a rotation may touch, so a
// bug here cannot reach any other field of the row. Both are in place -- the id
// and tenant are part of the associated data, so a delete-and-recreate would
// produce a value that no longer decrypts.
type Target interface {
	Enumerator
	RewrapAlertChannel(ctx context.Context, channel domain.AlertChannel) error
	RewrapPolicy(ctx context.Context, policy domain.MonitorPolicy) error
}

// Runner performs the rewrite. Keys is the same keyring the API writes with, so
// "the newest version" means the same thing here as it does on a live write.
type Runner struct {
	target Target
	keys   *security.Keyring
	logger *slog.Logger
}

func NewRunner(target Target, keys *security.Keyring, logger *slog.Logger) *Runner {
	return &Runner{target: target, keys: keys, logger: logger}
}

// Run walks every tenant and re-seals what it finds. With dryRun set it reports
// exactly what it would have changed and writes nothing.
//
// Rows are processed independently and a failure never aborts the pass: the
// operation is resumable by construction, so stopping at the first unreadable
// row would leave the rest of the estate un-rotated for no gain. A failure is
// collected and reported, and the caller re-runs.
func (r *Runner) Run(ctx context.Context, dryRun bool) (Stats, error) {
	var stats Stats
	// Fail closed on a missing keyring. The alternative -- treating every value
	// as current because there is no primary version to compare against -- would
	// report a successful rotation that never happened.
	if r.keys == nil {
		return stats, errors.New("rewrap needs a keyring: without one there is no primary key version to move values to")
	}
	tenants, _, err := r.target.ListTenants(ctx, store.ListOptions{})
	if err != nil {
		return stats, fmt.Errorf("list tenants: %w", err)
	}
	for _, tenant := range tenants {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		r.rewrapTenant(ctx, tenant, dryRun, &stats)
	}
	if len(stats.Failures) > 0 {
		return stats, fmt.Errorf("%d value(s) could not be rewrapped", len(stats.Failures))
	}
	return stats, nil
}

func (r *Runner) rewrapTenant(ctx context.Context, tenant domain.Tenant, dryRun bool, stats *Stats) {
	channels, err := r.target.ListAlertChannelsWithSecrets(ctx, tenant.ID)
	if err != nil {
		stats.Failures = append(stats.Failures, fmt.Sprintf("tenant %s: list alert channels: %v", tenant.ID, err))
		return
	}
	for _, channel := range channels {
		if err := ctx.Err(); err != nil {
			return
		}
		r.rewrapChannel(ctx, channel, dryRun, stats)
	}

	assets, _, err := r.target.ListAssets(ctx, tenant.ID, store.ListOptions{})
	if err != nil {
		stats.Failures = append(stats.Failures, fmt.Sprintf("tenant %s: list assets: %v", tenant.ID, err))
		return
	}
	for _, asset := range assets {
		if err := ctx.Err(); err != nil {
			return
		}
		r.rewrapPolicy(ctx, tenant, asset, dryRun, stats)
	}
}

// rewrapChannel re-seals the URL and the secret. The two fields are measured
// separately because they carry different associated data suffixes, so one can
// be current while the other is not -- a channel whose secret was rotated
// through the API but whose URL predates the key change is exactly that case.
func (r *Runner) rewrapChannel(ctx context.Context, channel domain.AlertChannel, dryRun bool, stats *Stats) {
	url, urlMoved, err := r.reseal(channel.URL, security.ChannelAssociatedData(channel.TenantID, channel.ID, "url"))
	if err != nil {
		stats.Failures = append(stats.Failures, fmt.Sprintf("alert channel %s url: %v", channel.ID, err))
		return
	}
	secret, secretMoved, err := r.reseal(channel.Secret, security.ChannelAssociatedData(channel.TenantID, channel.ID, "secret"))
	if err != nil {
		stats.Failures = append(stats.Failures, fmt.Sprintf("alert channel %s secret: %v", channel.ID, err))
		return
	}
	stats.ChannelsScanned++
	if !urlMoved && !secretMoved {
		stats.AlreadyCurrent++
		return
	}
	if dryRun {
		stats.ChannelsRewrapped++
		return
	}
	channel.URL, channel.Secret = url, secret
	if err := r.target.RewrapAlertChannel(ctx, channel); err != nil {
		stats.Failures = append(stats.Failures, fmt.Sprintf("alert channel %s: %v", channel.ID, err))
		return
	}
	stats.ChannelsRewrapped++
}

// rewrapPolicy re-seals the login credentials. A policy with no Auth, or no
// credentials stored, has nothing encrypted and is left out of the totals
// entirely rather than counted as current -- it holds no ciphertext, so it is
// not evidence about the state of the rotation either way.
func (r *Runner) rewrapPolicy(ctx context.Context, tenant domain.Tenant, asset domain.Asset, dryRun bool, stats *Stats) {
	policy, err := r.target.GetPolicy(ctx, tenant.ID, asset.ID)
	if err != nil {
		// An asset without a policy is the normal state before its first
		// detection, so a not-found is not a failure.
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		stats.Failures = append(stats.Failures, fmt.Sprintf("asset %s policy: %v", asset.ID, err))
		return
	}
	if policy.Auth == nil {
		return
	}
	associatedData := security.AuthAssociatedData(tenant.ID, asset.ID)
	username, usernameMoved, err := r.reseal(policy.Auth.EncryptedUsername, associatedData)
	if err != nil {
		stats.Failures = append(stats.Failures, fmt.Sprintf("asset %s username: %v", asset.ID, err))
		return
	}
	password, passwordMoved, err := r.reseal(policy.Auth.EncryptedPassword, associatedData)
	if err != nil {
		stats.Failures = append(stats.Failures, fmt.Sprintf("asset %s password: %v", asset.ID, err))
		return
	}
	stats.PoliciesScanned++
	if !usernameMoved && !passwordMoved {
		stats.AlreadyCurrent++
		return
	}
	if dryRun {
		stats.PoliciesRewrapped++
		return
	}
	policy.Auth.EncryptedUsername, policy.Auth.EncryptedPassword = username, password
	if err := r.target.RewrapPolicy(ctx, policy); err != nil {
		stats.Failures = append(stats.Failures, fmt.Sprintf("asset %s policy: %v", asset.ID, err))
		return
	}
	stats.PoliciesRewrapped++
}

// reseal returns the value re-encrypted under the primary version, and whether
// it had to change.
//
// Three cases are not "move it":
//
//   - An empty value has nothing to seal, and Encrypt returns "" for "", so
//     writing it back would be a no-op reported as a change.
//   - A value with no version prefix is what an install that never configured a
//     cipher stored. Re-sealing it is an upgrade of a different kind, and doing
//     it here would take on the risk of a write that changes what the row means
//     without an operator asking for it.
//   - A value already under the primary version is the state this pass exists to
//     reach. Re-sealing it would re-randomise the nonce on every run and make
//     the tool non-idempotent.
//
// A versioned value that cannot be decrypted is an error rather than a skip:
// that is a wrong or missing key, and reporting it as "already current" would
// hide a deployment that has lost the ability to read its own credentials.
func (r *Runner) reseal(stored, associatedData string) (string, bool, error) {
	if stored == "" {
		return "", false, nil
	}
	version, _, versioned := security.SplitKeyVersion(stored)
	if !versioned {
		return stored, false, nil
	}
	if version == r.keys.PrimaryVersion() {
		return stored, false, nil
	}
	plaintext, err := r.keys.Decrypt(stored, associatedData)
	if err != nil {
		return "", false, fmt.Errorf("decrypt v%d value: %w", version, err)
	}
	sealed, err := r.keys.Encrypt(plaintext, associatedData)
	if err != nil {
		return "", false, fmt.Errorf("re-encrypt v%d value: %w", version, err)
	}
	return sealed, true, nil
}
