package rewrap

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
	"webmonitoring/internal/migrate"
	"webmonitoring/internal/security"
	"webmonitoring/internal/store"
)

// firstTestKey is a fixed 32-byte key, base64 encoded. It is a test value and
// nothing else: the rotation test needs both versions to be known quantities.
const firstTestKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func v2KeyMaterial() string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = 0x40 + byte(i)
	}
	return "v2:" + base64.StdEncoding.EncodeToString(raw)
}

// TestRewrapAgainstPostgres is the case the unit tests cannot reach: the store
// derives the associated data itself, so a pass that agreed with the fake but not
// with channelAssociatedData would look correct in every unit test and would
// silently make production credentials undecryptable.
//
// The fixture writes a channel and a policy, rewraps them, and then checks the
// one thing that matters -- that the deployment can drop the retired key. That is
// asserted through a repository reopened on v2 only: if any stored value were
// still sealed under v1, opening it would fail here.
func TestRewrapAgainstPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_TEST_DSN"))
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	migrations, err := migrate.New(ctx, dsn)
	if err != nil {
		t.Fatalf("PostgreSQL integration test unavailable: %v", err)
	}
	defer migrations.Close()
	if err := migrations.Up(ctx, true); err != nil {
		t.Fatal(err)
	}

	tenantID := domain.DefaultTenantID
	assetID, channelID := identity.New(), identity.New()
	v1Material := "v1:" + firstTestKey
	rotatedMaterial := v1Material + "," + v2KeyMaterial()
	v2OnlyMaterial := v2KeyMaterial()

	// Write the rows on a v1-only repository: the state a deployment is in
	// immediately after raising ALERT_SECRET_KEYS, and before running this tool.
	writer, err := store.NewPostgresRepository(ctx, dsn, v1Material, store.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("PostgreSQL repository unavailable: %v", err)
	}
	v1Keys, err := security.NewKeyring(v1Material)
	if err != nil {
		t.Fatalf("build v1 keyring: %v", err)
	}
	// assets.owner_id is NOT NULL and references users(id), so the fixture has to
	// bring an owner of its own. The username carries the fresh asset id so a
	// re-run in a shared integration database does not collide on
	// (tenant_id, username).
	owner := domain.User{
		ID: identity.New(), TenantID: tenantID, Username: "rewrap-owner-" + assetID,
		PasswordHash: "integration-test-hash", Role: domain.RoleAdmin, CreatedAt: time.Now().UTC(),
	}
	if err := writer.CreateUser(context.Background(), owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := writer.CreateAsset(context.Background(), domain.Asset{
		ID: assetID, TenantID: tenantID, Name: "rewrap integration", URL: "https://rewrap-" + assetID + ".example",
		Type: domain.AssetOfficial, OwnerID: owner.ID, VerificationStatus: domain.VerificationPending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if err := writer.CreateAlertChannel(context.Background(), domain.AlertChannel{
		ID: channelID, TenantID: tenantID, Name: "rewrap integration", Type: "WEBHOOK",
		URL: "https://hooks.example.com/" + channelID, Secret: "integration-hmac",
		Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	auth := security.AuthAssociatedData(tenantID, assetID)
	sealedUsername, err := v1Keys.Encrypt("integration-operator", auth)
	if err != nil {
		t.Fatalf("seal username: %v", err)
	}
	sealedPassword, err := v1Keys.Encrypt("integration-password", auth)
	if err != nil {
		t.Fatalf("seal password: %v", err)
	}
	if err := writer.UpsertPolicy(context.Background(), domain.MonitorPolicy{
		ID: identity.New(), TenantID: tenantID, AssetID: assetID, Version: 1,
		Auth: &domain.AuthConfig{
			LoginURL: "https://example.com/login", CredentialsSet: true,
			EncryptedUsername: sealedUsername, EncryptedPassword: sealedPassword,
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	// A v2-only keyring cannot open a v1-sealed value: Keyring.Decrypt refuses a
	// version it does not hold. Asserting the fixture is genuinely un-rotated is
	// what stops a pass that did nothing from passing this test.
	//
	// The two row types are checked differently because they are read
	// differently. GetAlertChannel decrypts in the store and fails loudly on a
	// key it lacks, so reopening the row is the assertion. GetPolicy does not
	// decrypt at all -- it returns the stored JSON verbatim and the detection
	// checker decrypts the credentials later -- so the policy guard has to open
	// the sealed value the fixture wrote directly.
	v2Keys, err := security.NewKeyring(v2OnlyMaterial)
	if err != nil {
		t.Fatalf("build v2-only keyring: %v", err)
	}
	if _, err := v2Keys.Decrypt(sealedUsername, auth); err == nil {
		t.Fatal("the fixture credentials were already sealed under v2; the test proves nothing")
	}
	v2Reader, err := store.NewPostgresRepository(ctx, dsn, v2OnlyMaterial, store.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("open v2-only repository: %v", err)
	}
	if _, err := v2Reader.GetAlertChannel(context.Background(), tenantID, channelID); err == nil {
		t.Fatal("the fixture channel was already readable with v2 only; the test proves nothing")
	}
	v2Reader.Close()
	writer.Close()

	// Run the pass on the rotated keyring -- both versions configured, as they
	// are during a rotation -- then verify with the retired key gone.
	repository, err := store.NewPostgresRepository(ctx, dsn, rotatedMaterial, store.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	rotatedKeys, err := security.NewKeyring(rotatedMaterial)
	if err != nil {
		repository.Close()
		t.Fatalf("build rotated keyring: %v", err)
	}
	stats, err := NewRunner(repository, rotatedKeys, discardLogger()).Run(ctx, false)
	if err != nil {
		repository.Close()
		t.Fatalf("rewrap: %v", err)
	}
	// The database is shared with the other integration tests, so this asserts
	// "this row was included", never an exact total.
	if stats.ChannelsRewrapped < 1 || stats.PoliciesRewrapped < 1 {
		repository.Close()
		t.Fatalf("stats = %+v, want this test's channel and policy rewrapped", stats)
	}
	repository.Close()

	// The point of the whole pass: the retired key is no longer needed.
	v2Reader, err = store.NewPostgresRepository(ctx, dsn, v2OnlyMaterial, store.DefaultPoolConfig())
	if err != nil {
		t.Fatalf("reopen v2-only repository: %v", err)
	}
	defer v2Reader.Close()
	channel, err := v2Reader.GetAlertChannel(context.Background(), tenantID, channelID)
	if err != nil {
		t.Fatalf("v2-only repository cannot read the rewrapped channel: %v", err)
	}
	if channel.URL != "https://hooks.example.com/"+channelID || channel.Secret != "integration-hmac" {
		t.Fatalf("channel = %+v, want the original plaintext", channel)
	}
	policy, err := v2Reader.GetPolicy(context.Background(), tenantID, assetID)
	if err != nil {
		t.Fatalf("v2-only repository cannot read the rewrapped policy: %v", err)
	}
	if policy.Auth == nil || !policy.Auth.CredentialsSet {
		t.Fatalf("policy = %+v, want its credentials intact", policy)
	}
}
