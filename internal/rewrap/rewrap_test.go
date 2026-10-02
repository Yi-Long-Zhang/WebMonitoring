package rewrap

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

// testKey returns base64-encoded key material. Every key is distinct, so a
// ciphertext sealed under one and decrypted with another fails loudly rather
// than by luck.
func testKey(t *testing.T, fill byte) string {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill + byte(i)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// fakeTarget holds rows the way the store does: the credentials are ciphertext,
// so a pass that reports "rewrapped" without actually re-sealing is caught by
// the readback rather than trusted.
type fakeTarget struct {
	channels map[string]domain.AlertChannel
	policies map[string]domain.MonitorPolicy
	assets   map[string][]domain.Asset
	tenants  []domain.Tenant
	// failRewrapChannel and failRewrapPolicy make one write fail, so the
	// resumable path can be exercised without a real database.
	failRewrapChannel string
	failRewrapPolicy  string
	policyWrites      int
	channelWrites     int
}

func newFakeTarget() *fakeTarget {
	return &fakeTarget{
		channels: map[string]domain.AlertChannel{},
		policies: map[string]domain.MonitorPolicy{},
		assets:   map[string][]domain.Asset{},
	}
}

func (f *fakeTarget) ListTenants(context.Context, store.ListOptions) ([]domain.Tenant, string, error) {
	return f.tenants, "", nil
}

func (f *fakeTarget) ListAssets(_ context.Context, tenantID string, _ store.ListOptions) ([]domain.Asset, string, error) {
	return f.assets[tenantID], "", nil
}

func (f *fakeTarget) ListAlertChannelsEncrypted(_ context.Context, tenantID string) ([]domain.AlertChannel, error) {
	result := make([]domain.AlertChannel, 0)
	for _, channel := range f.channels {
		if channel.TenantID == tenantID {
			result = append(result, channel)
		}
	}
	return result, nil
}

func (f *fakeTarget) GetPolicy(_ context.Context, tenantID, assetID string) (domain.MonitorPolicy, error) {
	policy, ok := f.policies[tenantID+"/"+assetID]
	if !ok {
		return domain.MonitorPolicy{}, store.ErrNotFound
	}
	return policy, nil
}

func (f *fakeTarget) RewrapAlertChannel(_ context.Context, channel domain.AlertChannel) error {
	if channel.ID == f.failRewrapChannel {
		return errors.New("write refused")
	}
	f.channels[channel.TenantID+"/"+channel.ID] = channel
	f.channelWrites++
	return nil
}

func (f *fakeTarget) RewrapPolicy(_ context.Context, policy domain.MonitorPolicy) error {
	if policy.AssetID == f.failRewrapPolicy {
		return errors.New("write refused")
	}
	f.policies[policy.TenantID+"/"+policy.AssetID] = policy
	f.policyWrites++
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const (
	tenantID  = "11111111-1111-4111-8111-111111111111"
	assetID   = "22222222-2222-4222-8222-222222222222"
	channelID = "33333333-3333-4333-8333-333333333333"
)

// sealedFixture builds a two-version keyring and stores one channel and one
// policy whose credentials were sealed under v1, which is the state a deployment
// is in immediately after raising ALERT_SECRET_KEYS from v1 to v2.
func sealedFixture(t *testing.T) (*fakeTarget, *security.Keyring, *security.Keyring, *security.Keyring) {
	t.Helper()
	oldKeys, err := security.NewKeyring("v1:" + testKey(t, 0x10))
	if err != nil {
		t.Fatalf("build v1 keyring: %v", err)
	}
	rotated, err := security.NewKeyring("v1:" + testKey(t, 0x10) + ",v2:" + testKey(t, 0x40))
	if err != nil {
		t.Fatalf("build rotated keyring: %v", err)
	}
	// v2 only: the state a deployment reaches once it has dropped the retired
	// key, which is the point of the whole exercise.
	future, err := security.NewKeyring("v2:" + testKey(t, 0x40))
	if err != nil {
		t.Fatalf("build v2-only keyring: %v", err)
	}

	target := newFakeTarget()
	target.tenants = []domain.Tenant{{ID: tenantID, Slug: "acme", Name: "Acme"}}
	target.assets[tenantID] = []domain.Asset{{ID: assetID, TenantID: tenantID, Name: "homepage"}}

	url := security.ChannelAssociatedData(tenantID, channelID, "url")
	secret := security.ChannelAssociatedData(tenantID, channelID, "secret")
	encryptedURL, err := oldKeys.Encrypt("https://hooks.example.com/alert", url)
	if err != nil {
		t.Fatalf("seal url: %v", err)
	}
	encryptedSecret, err := oldKeys.Encrypt("shared-hmac-secret", secret)
	if err != nil {
		t.Fatalf("seal secret: %v", err)
	}
	target.channels[tenantID+"/"+channelID] = domain.AlertChannel{
		ID: channelID, TenantID: tenantID, Name: "webhook", Type: "WEBHOOK",
		URL: encryptedURL, Secret: encryptedSecret, Enabled: true,
	}

	auth := security.AuthAssociatedData(tenantID, assetID)
	encryptedUsername, err := oldKeys.Encrypt("operator", auth)
	if err != nil {
		t.Fatalf("seal username: %v", err)
	}
	encryptedPassword, err := oldKeys.Encrypt("correct-horse", auth)
	if err != nil {
		t.Fatalf("seal password: %v", err)
	}
	target.policies[tenantID+"/"+assetID] = domain.MonitorPolicy{
		ID: "44444444-4444-4444-8444-444444444444", TenantID: tenantID, AssetID: assetID,
		Auth: &domain.AuthConfig{
			LoginURL: "https://example.com/login", CredentialsSet: true,
			EncryptedUsername: encryptedUsername, EncryptedPassword: encryptedPassword,
		},
	}
	return target, oldKeys, rotated, future
}

func TestDryRunReportsWithoutWriting(t *testing.T) {
	target, _, rotated, _ := sealedFixture(t)
	before := target.channels[tenantID+"/"+channelID]

	stats, err := NewRunner(target, rotated, discardLogger()).Run(context.Background(), true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if stats.ChannelsRewrapped != 1 || stats.PoliciesRewrapped != 1 {
		t.Fatalf("stats = %+v, want one channel and one policy reported", stats)
	}
	if stats.ChannelsScanned != 1 || stats.PoliciesScanned != 1 {
		t.Fatalf("stats = %+v, want one of each scanned", stats)
	}
	if target.channelWrites != 0 || target.policyWrites != 0 {
		t.Fatalf("dry run wrote %d channel(s) and %d policy(ies)", target.channelWrites, target.policyWrites)
	}
	if after := target.channels[tenantID+"/"+channelID]; after.URL != before.URL || after.Secret != before.Secret {
		t.Fatal("dry run changed stored ciphertext")
	}
}

func TestRewrapMovesCiphertextToPrimaryVersion(t *testing.T) {
	target, _, rotated, future := sealedFixture(t)

	stats, err := NewRunner(target, rotated, discardLogger()).Run(context.Background(), false)
	if err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	if stats.Rewrapped() != true {
		t.Fatalf("stats = %+v, want a rewrapped result", stats)
	}

	channel := target.channels[tenantID+"/"+channelID]
	if version, _, ok := security.SplitKeyVersion(channel.URL); !ok || version != 2 {
		t.Fatalf("channel url version = %d (versioned=%v), want v2", version, ok)
	}
	if version, _, ok := security.SplitKeyVersion(channel.Secret); !ok || version != 2 {
		t.Fatalf("channel secret version = %d (versioned=%v), want v2", version, ok)
	}
	policy := target.policies[tenantID+"/"+assetID]
	if version, _, ok := security.SplitKeyVersion(policy.Auth.EncryptedUsername); !ok || version != 2 {
		t.Fatalf("policy username version = %d (versioned=%v), want v2", version, ok)
	}

	// The point of the rotation: the retired key is no longer needed. Decrypting
	// with a v2-only keyring succeeds, which is what makes dropping v1 possible.
	plaintextURL, err := future.Decrypt(channel.URL, security.ChannelAssociatedData(tenantID, channelID, "url"))
	if err != nil {
		t.Fatalf("v2-only keyring cannot read the rewrapped url: %v", err)
	}
	if plaintextURL != "https://hooks.example.com/alert" {
		t.Fatalf("url round trip = %q", plaintextURL)
	}
	plaintextSecret, err := future.Decrypt(channel.Secret, security.ChannelAssociatedData(tenantID, channelID, "secret"))
	if err != nil {
		t.Fatalf("v2-only keyring cannot read the rewrapped secret: %v", err)
	}
	if plaintextSecret != "shared-hmac-secret" {
		t.Fatalf("secret round trip = %q", plaintextSecret)
	}
	plaintextPassword, err := future.Decrypt(policy.Auth.EncryptedPassword, security.AuthAssociatedData(tenantID, assetID))
	if err != nil {
		t.Fatalf("v2-only keyring cannot read the rewrapped password: %v", err)
	}
	if plaintextPassword != "correct-horse" {
		t.Fatalf("password round trip = %q", plaintextPassword)
	}
}

func TestSecondPassIsNoOp(t *testing.T) {
	target, _, rotated, _ := sealedFixture(t)
	runner := NewRunner(target, rotated, discardLogger())
	if _, err := runner.Run(context.Background(), false); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	channelBefore := target.channels[tenantID+"/"+channelID]
	policyBefore := target.policies[tenantID+"/"+assetID]

	stats, err := runner.Run(context.Background(), false)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if stats.Rewrapped() {
		t.Fatalf("second pass rewrapped again: %+v", stats)
	}
	if stats.AlreadyCurrent != 2 {
		t.Fatalf("AlreadyCurrent = %d, want 2", stats.AlreadyCurrent)
	}
	// Idempotent means byte-identical, not merely still-decryptable: re-sealing
	// would draw a fresh nonce and the row would differ every run.
	if after := target.channels[tenantID+"/"+channelID]; after.URL != channelBefore.URL || after.Secret != channelBefore.Secret {
		t.Fatal("second pass re-sealed the channel")
	}
	if after := target.policies[tenantID+"/"+assetID]; after.Auth.EncryptedPassword != policyBefore.Auth.EncryptedPassword {
		t.Fatal("second pass re-sealed the policy")
	}
}

func TestRewrappedValueIsBoundToItsRow(t *testing.T) {
	target, _, rotated, future := sealedFixture(t)
	if _, err := NewRunner(target, rotated, discardLogger()).Run(context.Background(), false); err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	channel := target.channels[tenantID+"/"+channelID]

	// The associated data is what makes a copied ciphertext useless. If the pass
	// ever wrote a value under different associated data, this is the read that
	// would fail -- and with a v2-only keyring, nothing else could recover it.
	if _, err := future.Decrypt(channel.Secret, security.ChannelAssociatedData(tenantID, "other-channel", "secret")); err == nil {
		t.Fatal("channel secret decrypted under another channel's associated data")
	}
	policy := target.policies[tenantID+"/"+assetID]
	if _, err := future.Decrypt(policy.Auth.EncryptedPassword, security.AuthAssociatedData(tenantID, "other-asset")); err == nil {
		t.Fatal("policy password decrypted under another asset's associated data")
	}
}

func TestUnversionedValueIsLeftAlone(t *testing.T) {
	target, _, rotated, _ := sealedFixture(t)
	// A plaintext value is what an install that never configured a cipher
	// stored. Re-sealing it here would be a different operation than a rotation,
	// so the pass must not take it on.
	channel := target.channels[tenantID+"/"+channelID]
	channel.URL = "https://legacy.example.com/plain"
	target.channels[tenantID+"/"+channelID] = channel

	if _, err := NewRunner(target, rotated, discardLogger()).Run(context.Background(), false); err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	if got := target.channels[tenantID+"/"+channelID].URL; got != "https://legacy.example.com/plain" {
		t.Fatalf("unversioned url = %q, want it untouched", got)
	}
}

func TestUndecryptableValueIsAFailureNotASkip(t *testing.T) {
	target, _, rotated, _ := sealedFixture(t)
	channel := target.channels[tenantID+"/"+channelID]
	// Sealed under a key that is not configured at all. Reporting this as
	// "already current" would hide a deployment that cannot read its own
	// credentials.
	stranger, err := security.NewKeyring("v9:" + testKey(t, 0x90))
	if err != nil {
		t.Fatalf("build stranger keyring: %v", err)
	}
	channel.Secret, err = stranger.Encrypt("orphaned", security.ChannelAssociatedData(tenantID, channelID, "secret"))
	if err != nil {
		t.Fatalf("seal with stranger key: %v", err)
	}
	target.channels[tenantID+"/"+channelID] = channel

	stats, err := NewRunner(target, rotated, discardLogger()).Run(context.Background(), false)
	if err == nil {
		t.Fatal("expected an error for a value sealed under an unconfigured key")
	}
	if len(stats.Failures) != 1 || !strings.Contains(stats.Failures[0], "not configured") {
		t.Fatalf("failures = %v, want one naming the missing key", stats.Failures)
	}
	if stats.ChannelsRewrapped != 0 {
		t.Fatalf("ChannelsRewrapped = %d, want 0", stats.ChannelsRewrapped)
	}
}

func TestOneBadRowDoesNotStopThePass(t *testing.T) {
	target, _, rotated, _ := sealedFixture(t)
	// A second tenant, so there is work that must still happen after the first
	// row fails.
	otherTenant := "55555555-5555-4555-8555-555555555555"
	otherChannel := "66666666-6666-4666-8666-666666666666"
	oldKey, err := security.NewKeyring("v1:" + testKey(t, 0x10))
	if err != nil {
		t.Fatalf("build keyring: %v", err)
	}
	encryptedURL, err := oldKey.Encrypt("https://other.example.com/hook", security.ChannelAssociatedData(otherTenant, otherChannel, "url"))
	if err != nil {
		t.Fatalf("seal url: %v", err)
	}
	target.tenants = append(target.tenants, domain.Tenant{ID: otherTenant, Slug: "other", Name: "Other"})
	target.channels[otherTenant+"/"+otherChannel] = domain.AlertChannel{
		ID: otherChannel, TenantID: otherTenant, Name: "other", Type: "WEBHOOK", URL: encryptedURL,
	}
	target.failRewrapChannel = channelID

	stats, err := NewRunner(target, rotated, discardLogger()).Run(context.Background(), false)
	if err == nil {
		t.Fatal("expected an error reporting the failed row")
	}
	if len(stats.Failures) != 1 {
		t.Fatalf("failures = %v, want exactly one", stats.Failures)
	}
	// The other tenant's channel was still rotated: the pass is resumable, and
	// aborting on the first failure would leave the estate un-rotated.
	if version, _, ok := security.SplitKeyVersion(target.channels[otherTenant+"/"+otherChannel].URL); !ok || version != 2 {
		t.Fatalf("unrelated channel was not rewrapped (version=%d, versioned=%v)", version, ok)
	}
}

func TestPolicyWithoutCredentialsIsNotCounted(t *testing.T) {
	target, _, rotated, _ := sealedFixture(t)
	policy := target.policies[tenantID+"/"+assetID]
	policy.Auth = nil
	target.policies[tenantID+"/"+assetID] = policy

	stats, err := NewRunner(target, rotated, discardLogger()).Run(context.Background(), false)
	if err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	if stats.PoliciesScanned != 0 || stats.PoliciesRewrapped != 0 {
		t.Fatalf("stats = %+v, want the credential-less policy left out of the totals", stats)
	}
	if stats.ChannelsRewrapped != 1 {
		t.Fatalf("stats = %+v, want the channel still rewrapped", stats)
	}
}

func TestMissingPolicyIsNotAFailure(t *testing.T) {
	target, _, rotated, _ := sealedFixture(t)
	delete(target.policies, tenantID+"/"+assetID)

	stats, err := NewRunner(target, rotated, discardLogger()).Run(context.Background(), false)
	if err != nil {
		t.Fatalf("an asset without a policy must not be an error: %v", err)
	}
	if len(stats.Failures) != 0 {
		t.Fatalf("failures = %v, want none", stats.Failures)
	}
}

func TestMissingKeyringFailsClosed(t *testing.T) {
	// The CLI guards this too, but a pass that treated a missing keyring as
	// "nothing to compare against, so everything is current" would report a
	// successful rotation that never happened, which is the worst outcome here.
	_, err := NewRunner(newFakeTarget(), nil, discardLogger()).Run(context.Background(), false)
	if err == nil {
		t.Fatal("expected a missing keyring to be refused")
	}
	if !strings.Contains(err.Error(), "keyring") {
		t.Fatalf("error = %v, want it to name the keyring", err)
	}
}

func TestStatsRewrappedCoverage(t *testing.T) {
	if (Stats{}).Rewrapped() {
		t.Fatal("zero stats must not report a rewrapped result")
	}
	if !(Stats{PoliciesRewrapped: 1}).Rewrapped() {
		t.Fatal("a rewrapped policy must report one")
	}
}
