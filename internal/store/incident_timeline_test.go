package store

import (
	"context"
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
	if err := repository.CreateTenant(context.Background(), tenant); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	owner := domain.User{
		ID: identity.New(), TenantID: tenant.ID, Username: "owner-" + suffix,
		PasswordHash: "test-hash", Role: domain.RoleAdmin, CreatedAt: now,
	}
	if err := repository.CreateUser(context.Background(), owner); err != nil {
		t.Fatalf("create user: %v", err)
	}
	asset := domain.Asset{
		ID: identity.New(), TenantID: tenant.ID, Name: "asset-" + suffix,
		URL: "https://timeline-" + suffix + ".example", Type: domain.AssetOfficial,
		OwnerID: owner.ID, Enabled: true, VerificationStatus: domain.VerificationVerified,
		VerificationMethod: domain.VerificationFile, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateAsset(context.Background(), asset); err != nil {
		t.Fatalf("create asset: %v", err)
	}
	baseline := domain.BaselineVersion{
		ID: identity.New(), TenantID: tenant.ID, AssetID: asset.ID, Version: 1,
		Status: domain.BaselineApproved, CreatedBy: owner.ID, CreatedAt: now,
	}
	if err := repository.CreateBaseline(context.Background(), baseline); err != nil {
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

			if _, outcome, err := repository.UpsertIncident(context.Background(),
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("first")}, []string{"ev-1"}, clock()),
			); err != nil || outcome != IncidentCreated {
				t.Fatalf("first round: outcome=%v err=%v", outcome, err)
			}
			if _, outcome, err := repository.UpsertIncident(context.Background(),
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityMedium, []domain.Signal{signal("second")}, []string{"ev-2"}, clock()),
			); err != nil || outcome != IncidentRecurred {
				t.Fatalf("second round: outcome=%v err=%v", outcome, err)
			}
			merged, outcome, err := repository.UpsertIncident(context.Background(),
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityHigh, []domain.Signal{signal("third")}, []string{"ev-3"}, clock()),
			)
			if err != nil || outcome != IncidentRecurred {
				t.Fatalf("third round: outcome=%v err=%v", outcome, err)
			}

			events, _, err := repository.ListIncidentEvents(context.Background(), tenantID, merged.ID, ListOptions{})
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
			first, _, err := repository.UpsertIncident(context.Background(),
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("a")}, []string{"ev-1"}, clock()))
			if err != nil {
				t.Fatal(err)
			}
			round := incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("b")}, []string{"ev-2"}, clock())
			merged, _, err := repository.UpsertIncident(context.Background(), round)
			if err != nil {
				t.Fatal(err)
			}
			if round.ID == first.ID {
				t.Fatal("fixture produced the same id twice")
			}
			events, _, err := repository.ListIncidentEvents(context.Background(), tenantID, first.ID, ListOptions{})
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

// A recurrence on a closed incident reopens it: same id, same timeline, status
// back to OPEN, and a REOPENED entry appended.
//
// This test used to assert the opposite -- that the recurrence produced a second
// incident with its own CREATED timeline -- because the dedup index was partial
// and simply did not match closed rows. That meant one problem an operator had
// already dealt with could be sitting in the list twice, as two rows with the
// same fingerprint and no link between them. The merge below is what migration
// 000015 makes possible; the timeline keeps its history rather than restarting.
func TestClosedIncidentRecurrenceReopensTheSameIncident(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID, baselineID := seedIncidentFixture(t, repository)
			fingerprint := identity.New()
			clock := roundClock()
			first, _, err := repository.UpsertIncident(context.Background(),
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("a")}, []string{"ev-1"}, clock()))
			if err != nil {
				t.Fatal(err)
			}
			first.Status = domain.IncidentClosed
			if err := repository.UpdateIncident(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			reopened, outcome, err := repository.UpsertIncident(context.Background(),
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("b")}, []string{"ev-2"}, clock()))
			if err != nil || outcome != IncidentReopened {
				t.Fatalf("recurrence after close: outcome=%v err=%v", outcome, err)
			}
			if reopened.ID != first.ID {
				t.Fatalf("reopen made a new incident %q, want the closed one %q", reopened.ID, first.ID)
			}
			if reopened.Status != domain.IncidentOpen {
				t.Fatalf("reopened status = %q, want OPEN", reopened.Status)
			}
			if reopened.OccurrenceCount != 2 {
				t.Fatalf("reopened occurrence count = %d, want 2", reopened.OccurrenceCount)
			}
			events, _, err := repository.ListIncidentEvents(context.Background(), tenantID, first.ID, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			// Newest first: the reopen, then the original creation. The pre-close
			// history is still there -- it was not moved to a second incident.
			if len(events) != 2 {
				t.Fatalf("timeline = %d entries, want 2", len(events))
			}
			if events[0].Type != domain.IncidentEventReopened {
				t.Fatalf("newest entry = %q, want REOPENED", events[0].Type)
			}
			if events[1].Type != domain.IncidentEventCreated {
				t.Fatalf("oldest entry = %q, want CREATED", events[1].Type)
			}
		})
	}
}

// A dismissed incident (FALSE_POSITIVE) reopens the same way. It is a separate
// case because the operator's intent differs -- "this was never a real problem"
// rather than "this was fixed" -- but the storage path is identical, and if the
// predicate ever drifts to name only CLOSED this catches it.
func TestFalsePositiveIncidentRecurrenceReopensTheSameIncident(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			tenantID, assetID, baselineID := seedIncidentFixture(t, repository)
			fingerprint := identity.New()
			clock := roundClock()
			first, _, err := repository.UpsertIncident(context.Background(),
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("a")}, []string{"ev-1"}, clock()))
			if err != nil {
				t.Fatal(err)
			}
			first.Status = domain.IncidentFalsePositive
			if err := repository.UpdateIncident(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			reopened, outcome, err := repository.UpsertIncident(context.Background(),
				incidentRound(tenantID, assetID, baselineID, fingerprint, domain.SeverityLow, []domain.Signal{signal("b")}, []string{"ev-2"}, clock()))
			if err != nil || outcome != IncidentReopened {
				t.Fatalf("recurrence after dismissal: outcome=%v err=%v", outcome, err)
			}
			if reopened.ID != first.ID || reopened.Status != domain.IncidentOpen {
				t.Fatalf("reopened = %#v, want the dismissed incident back at OPEN", reopened)
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

			if _, _, err := repository.UpsertIncident(context.Background(), incidentRound(
				tenantID, assetID, baselineID, fingerprint, domain.SeverityHigh,
				[]domain.Signal{signal("first")}, []string{"ev-1", "ev-2", "ev-1"}, clock())); err != nil {
				t.Fatal(err)
			}
			merged, _, err := repository.UpsertIncident(context.Background(), incidentRound(
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
			incident, _, err := repository.UpsertIncident(context.Background(), incidentRound(
				tenantID, assetID, baselineID, identity.New(), domain.SeverityLow,
				[]domain.Signal{signal("a")}, []string{"ev-1"}, roundClock()()))
			if err != nil {
				t.Fatal(err)
			}

			missing := domain.IncidentEvent{
				ID: identity.New(), TenantID: tenantID, IncidentID: identity.New(),
				Type: domain.IncidentEventCommented, CreatedAt: time.Now().UTC(),
			}
			if err := repository.AppendIncidentEvent(context.Background(), missing); !errors.Is(err, ErrNotFound) {
				t.Fatalf("entry for a missing incident: err = %v, want ErrNotFound", err)
			}
			crossTenant := missing
			crossTenant.IncidentID = incident.ID
			crossTenant.TenantID = otherTenant
			if err := repository.AppendIncidentEvent(context.Background(), crossTenant); !errors.Is(err, ErrNotFound) {
				t.Fatalf("cross-tenant entry: err = %v, want ErrNotFound", err)
			}
			if _, _, err := repository.ListIncidentEvents(context.Background(), tenantID, incident.ID, ListOptions{}); err != nil {
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
			first, _, err := repository.UpsertIncident(context.Background(), incidentRound(
				tenantID, assetID, baselineID, identity.New(), domain.SeverityLow,
				[]domain.Signal{signal("a")}, []string{"ev-1"}, clock()))
			if err != nil {
				t.Fatal(err)
			}
			other, _, err := repository.UpsertIncident(context.Background(), incidentRound(
				otherTenant, otherAsset, otherBaseline, identity.New(), domain.SeverityLow,
				[]domain.Signal{signal("b")}, []string{"ev-2"}, clock()))
			if err != nil {
				t.Fatal(err)
			}
			for index := 0; index < 4; index++ {
				if err := repository.AppendIncidentEvent(context.Background(), domain.IncidentEvent{
					ID: identity.New(), TenantID: tenantID, IncidentID: first.ID,
					Type: domain.IncidentEventCommented, Details: map[string]any{"note": "seen"},
					CreatedAt: clock(),
				}); err != nil {
					t.Fatal(err)
				}
			}

			events, _, err := repository.ListIncidentEvents(context.Background(), tenantID, first.ID, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 5 {
				t.Fatalf("entries = %d, want 5", len(events))
			}
			// Another incident's timeline must not leak in, in either direction.
			otherEvents, _, err := repository.ListIncidentEvents(context.Background(), otherTenant, other.ID, ListOptions{})
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
				items, next, err := repository.ListIncidentEvents(context.Background(), tenantID, first.ID, ListOptions{Limit: 2, Cursor: cursor})
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

// CountOpenIncidents backs the /metrics gauges, so it must agree across stores
// and must count across tenants (a scrape carries no tenant context) while
// excluding closed and dismissed incidents from every tier.
//
// The count is platform-wide by design, so the shared integration database
// already holds other tests' incidents. The assertions measure the delta this
// test creates rather than an absolute value, which would otherwise depend on
// what ran before it.
func TestCountOpenIncidentsAgreesAcrossStores(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			beforeOpen, beforeHighRisk, beforeCritical, err := repository.CountOpenIncidents(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			tenantA, assetA, baselineA := seedIncidentFixture(t, repository)
			tenantB, assetB, baselineB := seedIncidentFixture(t, repository)
			clock := roundClock()

			if _, _, err := repository.UpsertIncident(context.Background(), incidentRound(
				tenantA, assetA, baselineA, identity.New(), domain.SeverityLow,
				[]domain.Signal{signal("low")}, nil, clock())); err != nil {
				t.Fatal(err)
			}
			if _, _, err := repository.UpsertIncident(context.Background(), incidentRound(
				tenantA, assetA, baselineA, identity.New(), domain.SeverityCritical,
				[]domain.Signal{signal("critical")}, nil, clock())); err != nil {
				t.Fatal(err)
			}
			closedHigh, _, err := repository.UpsertIncident(context.Background(), incidentRound(
				tenantA, assetA, baselineA, identity.New(), domain.SeverityHigh,
				[]domain.Signal{signal("closed")}, nil, clock()))
			if err != nil {
				t.Fatal(err)
			}
			closedHigh.Status = domain.IncidentClosed
			if err := repository.UpdateIncident(context.Background(), closedHigh); err != nil {
				t.Fatal(err)
			}
			if _, _, err := repository.UpsertIncident(context.Background(), incidentRound(
				tenantB, assetB, baselineB, identity.New(), domain.SeverityMedium,
				[]domain.Signal{signal("other-tenant")}, nil, clock())); err != nil {
				t.Fatal(err)
			}

			open, highRisk, critical, err := repository.CountOpenIncidents(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if open-beforeOpen != 3 {
				t.Fatalf("open grew by %d, want 3 (low + critical + other tenant; the closed HIGH must not count)", open-beforeOpen)
			}
			if highRisk-beforeHighRisk != 1 {
				t.Fatalf("highRisk grew by %d, want 1 (only the open CRITICAL; closed HIGH excluded)", highRisk-beforeHighRisk)
			}
			if critical-beforeCritical != 1 {
				t.Fatalf("critical grew by %d, want 1", critical-beforeCritical)
			}
		})
	}
}
