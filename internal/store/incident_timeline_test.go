package store

import (
	"errors"
	"slices"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

// seedIncidentFixture creates the rows an incident references, and returns the
// tenant, asset and baseline ids. Incidents need all three because of the
// same-tenant composite foreign keys.
func seedIncidentFixture(t *testing.T, repository Repository) (string, string, string) {
	t.Helper()
	now := time.Now().UTC()
	suffix := identity.New()
	tenant := domain.Tenant{ID: identity.New(), Slug: "timeline-" + suffix, Name: "timeline", CreatedAt: now}
	if err := repository.CreateTenant(tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	owner := domain.User{
		ID: identity.New(), TenantID: tenant.ID, Username: "owner-" + suffix,
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: now,
	}
	if err := repository.CreateUser(owner); err != nil {
		t.Fatalf("create user: %v", err)
	}
	asset := domain.Asset{
		ID: identity.New(), TenantID: tenant.ID, Name: "asset-" + suffix,
		URL: "https://timeline-" + suffix + ".example", Type: domain.AssetOfficial,
		OwnerID: owner.ID, Enabled: true, VerificationStatus: domain.VerificationVerified,
		VerificationMethod: domain.VerificationFile, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAsset(asset); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	baseline := domain.BaselineVersion{
		ID: identity.New(), TenantID: tenant.ID, AssetID: asset.ID, Version: 1,
		Status: domain.BaselineApproved, CreatedBy: owner.ID, CreatedAt: now,
	}
	if err := repository.CreateBaseline(baseline); err != nil {
		t.Fatalf("create baseline: %v", err)
	}
	return tenant.ID, asset.ID, baseline.ID
}

// incidentRound builds one detection round for a fixed fingerprint at a given
// instant. The instant is explicit because timeline entries are ordered by it:
// rounds in the same millisecond would order arbitrarily, and a test that does
// not control the clock would be asserting on a coin flip.
func incidentRound(tenantID, assetID, baselineID, fingerprint string, severity domain.Severity, signals []domain.Signal, evidence []string, at time.Time) domain.Incident {
	return domain.Incident{
		ID: identity.New(), TenantID: tenantID, AssetID: assetID,
		BaselineID: baselineID, BaselineVersion: 1, Fingerprint: fingerprint,
		Severity: severity, Status: domain.IncidentOpen, Title: "changed",
		Signals: signals, EvidenceIDs: evidence, OccurrenceCount: 1,
		CreatedAt: at, FirstSeenAt: at, LastSeenAt: at,
	}
}

// roundClock hands out strictly increasing instants.
func roundClock() func() time.Time {
	base := time.Now().UTC().Add(-time.Hour)
	step := 0
	return func() time.Time {
		step++
		return base.Add(time.Duration(step) * time.Second)
	}
}

func signal(kind string) domain.Signal {
	return domain.Signal{Kind: kind, Severity: domain.SeverityMedium, Summary: kind, RuleVersion: "v1", Fingerprint: kind}
}

// TestRecurrenceKeepsPerOccurrenceSignals is the regression test for the whole
// feature. UpsertIncident used to write signals=EXCLUDED.signals, so every
// recurrence overwrote the previous signal set and the second and later
// occurrences survived only as a number in occurrence_count.
func TestRecurrenceKeepsPerOccurrenceSignals(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID, baselineID := seedIncidentFixture(t, repository)
			fingerprint := identity.New()
			clock := roundClock()

			if _, created, err := repository.UpsertIncident(
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("first")}, []string{"ev-1"}, clock()),
			); err != nil || !created {
				t.Fatalf("first round: created=%v err=%v", created, err)
			}
			if _, created, err := repository.UpsertIncident(
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityMedium, []domain.Signal{signal("second")}, []string{"ev-2"}, clock()),
			); err != nil || created {
				t.Fatalf("second round: created=%v err=%v", created, err)
			}
			merged, created, err := repository.UpsertIncident(
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityHigh, []domain.Signal{signal("third")}, []string{"ev-3"}, clock()),
			)
			if err != nil || created {
				t.Fatalf("third round: created=%v err=%v", created, err)
			}

			events, _, err := repository.ListIncidentEvents(tenantID, merged.ID, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 3 {
				t.Fatalf("timeline entries = %d, want 3", len(events))
			}
			// Newest first.
			wantSignals := []string{"third", "second", "first"}
			for index, want := range wantSignals {
				if len(events[index].Signals) != 1 || events[index].Signals[0].Kind != want {
					t.Fatalf("entry %d signals = %+v, want kind %q", index, events[index].Signals, want)
				}
			}
			if events[2].Type != domain.IncidentEventCreated {
				t.Fatalf("oldest entry type = %s, want CREATED", events[2].Type)
			}
			if events[0].Type != domain.IncidentEventRecurred || events[1].Type != domain.IncidentEventRecurred {
				t.Fatalf("later entry types = %s, %s, want RECURRED", events[0].Type, events[1].Type)
			}
			// The incident row itself keeps only the latest round, as before.
			if len(merged.Signals) != 1 || merged.Signals[0].Kind != "third" {
				t.Fatalf("incident signals = %+v, want only the latest round", merged.Signals)
			}
		})
	}
}

// A round generates a fresh incident ID that the upsert discards when it merges.
// The timeline entry has to hang off the incident that actually exists.
func TestRecurrenceEntryUsesTheMergedIncidentID(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID, baselineID := seedIncidentFixture(t, repository)
			fingerprint := identity.New()
			clock := roundClock()
			first, _, err := repository.UpsertIncident(
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("a")}, []string{"ev-1"}, clock()))
			if err != nil {
				t.Fatal(err)
			}
			round := incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("b")}, []string{"ev-2"}, clock())
			merged, _, err := repository.UpsertIncident(round)
			if err != nil {
				t.Fatal(err)
			}
			if round.ID == first.ID {
				t.Fatal("fixture produced the same id twice")
			}
			events, _, err := repository.ListIncidentEvents(tenantID, first.ID, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 {
				t.Fatalf("entries on the merged incident = %d, want 2", len(events))
			}
			for _, event := range events {
				if event.IncidentID != first.ID {
					t.Fatalf("entry points at %s, want the merged id %s", event.IncidentID, first.ID)
				}
			}
			if merged.ID != first.ID {
				t.Fatalf("merge returned %s, want %s", merged.ID, first.ID)
			}
		})
	}
}

// The dedup index only covers active incidents, so a closed incident followed by
// the same change is a new incident with its own timeline. The old one must not
// gain entries.
func TestClosedIncidentRecurrenceStartsANewTimeline(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID, baselineID := seedIncidentFixture(t, repository)
			fingerprint := identity.New()
			clock := roundClock()
			first, _, err := repository.UpsertIncident(
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("a")}, []string{"ev-1"}, clock()))
			if err != nil {
				t.Fatal(err)
			}
			first.Status = domain.IncidentClosed
			if err := repository.UpdateIncident(first); err != nil {
				t.Fatal(err)
			}
			second, created, err := repository.UpsertIncident(
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("b")}, []string{"ev-2"}, clock()))
			if err != nil || !created {
				t.Fatalf("recurrence after close: created=%v err=%v", created, err)
			}
			if second.ID == first.ID {
				t.Fatal("recurrence reused the closed incident")
			}
			oldEvents, _, err := repository.ListIncidentEvents(tenantID, first.ID, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(oldEvents) != 1 {
				t.Fatalf("closed incident timeline = %d entries, want 1", len(oldEvents))
			}
			newEvents, _, err := repository.ListIncidentEvents(tenantID, second.ID, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(newEvents) != 1 || newEvents[0].Type != domain.IncidentEventCreated {
				t.Fatalf("new incident timeline = %+v, want one CREATED entry", newEvents)
			}
		})
	}
}

// The two stores must merge identically. They did not: PostgreSQL allowed
// severity to fall back between non-CRITICAL levels while memory kept it
// monotonic, and it kept duplicate evidence ids that memory deduplicated. Both
// subtests below assert the same expected values, so a divergence shows up as
// one implementation failing rather than as two subtly different incidents.
func TestIncidentMergeAgreesAcrossStores(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID, baselineID := seedIncidentFixture(t, repository)
			fingerprint := identity.New()
			clock := roundClock()

			if _, _, err := repository.UpsertIncident(incidentRound(
				tenantID, assetID, baselineID, fingerprint, domain.SeverityHigh,
				[]domain.Signal{signal("first")}, []string{"ev-1", "ev-2", "ev-1"}, clock())); err != nil {
				t.Fatal(err)
			}
			merged, _, err := repository.UpsertIncident(incidentRound(
				tenantID, assetID, baselineID, fingerprint, domain.SeverityLow,
				[]domain.Signal{signal("second")}, []string{"ev-2", "ev-3"}, clock()))
			if err != nil {
				t.Fatal(err)
			}
			if merged.Severity != domain.SeverityHigh {
				t.Fatalf("severity = %s, want HIGH: an incident must not de-escalate", merged.Severity)
			}
			want := []string{"ev-1", "ev-2", "ev-3"}
			if !slices.Equal(merged.EvidenceIDs, want) {
				t.Fatalf("evidence ids = %v, want %v (deduplicated and ordered)", merged.EvidenceIDs, want)
			}
			if merged.OccurrenceCount != 2 {
				t.Fatalf("occurrence count = %d, want 2", merged.OccurrenceCount)
			}
		})
	}
}

// An entry may only land on an incident that exists in the same tenant. The
// PostgreSQL implementation answers with a zero row count rather than letting
// the foreign key raise 23503, which the API would surface as a 500.
func TestAppendIncidentEventRequiresAnIncidentInTheTenant(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID, baselineID := seedIncidentFixture(t, repository)
			otherTenant, _, _ := seedIncidentFixture(t, repository)
			incident, _, err := repository.UpsertIncident(incidentRound(
				tenantID, assetID, baselineID, identity.New(), domain.SeverityLow,
				[]domain.Signal{signal("a")}, []string{"ev-1"}, roundClock()()))
			if err != nil {
				t.Fatal(err)
			}

			missing := domain.IncidentEvent{
				ID: identity.New(), TenantID: tenantID, IncidentID: identity.New(),
				Type: domain.IncidentEventCommented, CreatedAt: time.Now().UTC(),
			}
			if err := repository.AppendIncidentEvent(missing); !errors.Is(err, ErrNotFound) {
				t.Fatalf("entry for a missing incident: err = %v, want ErrNotFound", err)
			}
			crossTenant := missing
			crossTenant.IncidentID = incident.ID
			crossTenant.TenantID = otherTenant
			if err := repository.AppendIncidentEvent(crossTenant); !errors.Is(err, ErrNotFound) {
				t.Fatalf("cross-tenant entry: err = %v, want ErrNotFound", err)
			}
			if _, _, err := repository.ListIncidentEvents(tenantID, incident.ID, ListOptions{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestListIncidentEventsIsolatesIncidentsAndPages(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID, baselineID := seedIncidentFixture(t, repository)
			otherTenant, otherAsset, otherBaseline := seedIncidentFixture(t, repository)
			clock := roundClock()
			first, _, err := repository.UpsertIncident(incidentRound(
				tenantID, assetID, baselineID, identity.New(), domain.SeverityLow,
				[]domain.Signal{signal("a")}, []string{"ev-1"}, clock()))
			if err != nil {
				t.Fatal(err)
			}
			other, _, err := repository.UpsertIncident(incidentRound(
				otherTenant, otherAsset, otherBaseline, identity.New(), domain.SeverityLow,
				[]domain.Signal{signal("b")}, []string{"ev-2"}, clock()))
			if err != nil {
				t.Fatal(err)
			}
			for index := 0; index < 4; index++ {
				if err := repository.AppendIncidentEvent(domain.IncidentEvent{
					ID: identity.New(), TenantID: tenantID, IncidentID: first.ID,
					Type: domain.IncidentEventCommented, Details: map[string]any{"note": "seen"},
					CreatedAt: clock(),
				}); err != nil {
					t.Fatal(err)
				}
			}

			events, _, err := repository.ListIncidentEvents(tenantID, first.ID, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 5 {
				t.Fatalf("entries = %d, want 5", len(events))
			}
			// Another incident's timeline must not leak in, in either direction.
			otherEvents, _, err := repository.ListIncidentEvents(otherTenant, other.ID, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(otherEvents) != 1 {
				t.Fatalf("other incident entries = %d, want 1", len(otherEvents))
			}

			// Walking the keyset pages must visit every entry exactly once.
			seen := map[string]bool{}
			cursor := ""
			for page := 0; page < 10; page++ {
				items, next, err := repository.ListIncidentEvents(tenantID, first.ID, ListOptions{Limit: 2, Cursor: cursor})
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range items {
					if seen[item.ID] {
						t.Fatalf("entry %s returned twice", item.ID)
					}
					seen[item.ID] = true
				}
				if next == "" {
					break
				}
				cursor = next
			}
			if len(seen) != 5 {
				t.Fatalf("paged over %d entries, want 5", len(seen))
			}
		})
	}
}
