package store

import (
	"context"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

type incidentFilterFixture struct {
	tenantID string
	assetA   string
	assetB   string
	ownerA   string
	byLabel  map[string]string
	allIDs   map[string]bool
	start    time.Time
	mid      time.Time
	end      time.Time
}

func (f incidentFilterFixture) ids(labels ...string) map[string]bool {
	result := make(map[string]bool, len(labels))
	for _, label := range labels {
		result[f.byLabel[label]] = true
	}
	return result
}

// seedIncidentFilterFixture lays out incidents that differ along every filter
// dimension, spread over two assets of one tenant so an asset predicate has
// something to discriminate, plus one matching incident in a second tenant so
// the tenant predicate can be shown to survive the new ones.
func seedIncidentFilterFixture(t *testing.T, repository Repository) incidentFilterFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := identity.New()
	tenant := domain.Tenant{ID: identity.New(), Slug: "incident-filter-" + suffix, Name: "filter", CreatedAt: now}
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
	other := domain.User{
		ID: identity.New(), TenantID: tenant.ID, Username: "other-" + suffix,
		PasswordHash: "test-hash", Role: domain.RoleOperator, CreatedAt: now,
	}
	if err := repository.CreateUser(context.Background(), other); err != nil {
		t.Fatalf("create user: %v", err)
	}
	newAsset := func(name string) (string, string) {
		asset := domain.Asset{
			ID: identity.New(), TenantID: tenant.ID, Name: name,
			URL: "https://" + name + "-" + suffix + ".example", Type: domain.AssetOfficial,
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
		return asset.ID, baseline.ID
	}
	assetA, baselineA := newAsset("filter-a")
	assetB, baselineB := newAsset("filter-b")

	fixture := incidentFilterFixture{
		tenantID: tenant.ID, assetA: assetA, assetB: assetB, ownerA: owner.ID,
		byLabel: map[string]string{},
	}
	seed := func(label, assetID, baselineID, fingerprint string, severity domain.Severity, status domain.IncidentStatus,
		ownerID, title string, lastSeen time.Time) {
		t.Helper()
		saved, _, err := repository.UpsertIncident(context.Background(), domain.Incident{
			ID: identity.New(), TenantID: tenant.ID, AssetID: assetID, BaselineID: baselineID,
			BaselineVersion: 1, Fingerprint: fingerprint, Severity: severity, Status: domain.IncidentOpen,
			Title: title, Signals: []domain.Signal{signal("tamper")}, OccurrenceCount: 1,
			CreatedAt: lastSeen, FirstSeenAt: lastSeen, LastSeenAt: lastSeen,
		})
		if err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
		saved.Status = status
		saved.OwnerID = ownerID
		saved.Title = title
		saved.LastSeenAt = lastSeen
		if err := repository.UpdateIncident(context.Background(), saved); err != nil {
			t.Fatalf("update %s: %v", label, err)
		}
		fixture.byLabel[label] = saved.ID
	}
	base := now.Add(-48 * time.Hour)
	seed("openHigh", assetA, baselineA, "fp-filter-open-high", domain.SeverityHigh, domain.IncidentOpen, fixture.ownerA, "网页被篡改 detected", base.Add(time.Hour))
	seed("closedCritical", assetA, baselineA, "fp-filter-closed-crit", domain.SeverityCritical, domain.IncidentClosed, other.ID, "注入尝试", base.Add(2*time.Hour))
	seed("openLow", assetB, baselineB, "fp-filter-open-low", domain.SeverityLow, domain.IncidentOpen, "", "页脚小改动", base.Add(3*time.Hour))
	seed("falseMedium", assetB, baselineB, "fp-filter-false-med", domain.SeverityMedium, domain.IncidentFalsePositive, fixture.ownerA, "误报 Sample 页", base.Add(4*time.Hour))

	fixture.allIDs = map[string]bool{}
	for _, id := range fixture.byLabel {
		fixture.allIDs[id] = true
	}
	fixture.start = base.Add(30 * time.Minute)
	fixture.mid = base.Add(2*time.Hour + 30*time.Minute)
	fixture.end = base.Add(4*time.Hour + 30*time.Minute)
	return fixture
}

func incidentIDs(incidents []domain.Incident) map[string]bool {
	result := make(map[string]bool, len(incidents))
	for _, incident := range incidents {
		result[incident.ID] = true
	}
	return result
}

// Both stores implement the incident predicates -- one in Go, one in SQL -- so
// nothing but this test keeps the two definitions aligned. A filter that drifted
// would silently show a different list depending on which store the deployment
// happened to be running.
func TestIncidentFilterAgreesAcrossStores(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedIncidentFilterFixture(t, repository)
			cases := []struct {
				label  string
				filter IncidentFilter
				want   map[string]bool
			}{
				{"no filter", IncidentFilter{}, fixture.allIDs},
				{"status OPEN", IncidentFilter{Status: "OPEN"}, fixture.ids("openHigh", "openLow")},
				{"status CLOSED", IncidentFilter{Status: "CLOSED"}, fixture.ids("closedCritical")},
				{"severity HIGH", IncidentFilter{Severity: "HIGH"}, fixture.ids("openHigh")},
				{"severity CRITICAL", IncidentFilter{Severity: "CRITICAL"}, fixture.ids("closedCritical")},
				{"asset A", IncidentFilter{AssetID: fixture.assetA}, fixture.ids("openHigh", "closedCritical")},
				{"asset B", IncidentFilter{AssetID: fixture.assetB}, fixture.ids("openLow", "falseMedium")},
				{"owner A", IncidentFilter{OwnerID: fixture.ownerA}, fixture.ids("openHigh", "falseMedium")},
				{"keyword 篡改", IncidentFilter{Keyword: "篡改"}, fixture.ids("openHigh")},
				{"keyword case-insensitive", IncidentFilter{Keyword: "sample"}, fixture.ids("falseMedium")},
				{
					"status+severity",
					IncidentFilter{Status: "OPEN", Severity: "HIGH"},
					fixture.ids("openHigh"),
				},
				{
					"asset+status",
					IncidentFilter{AssetID: fixture.assetB, Status: "OPEN"},
					fixture.ids("openLow"),
				},
				{
					"half-open range",
					IncidentFilter{From: fixture.mid},
					fixture.ids("openLow", "falseMedium"),
				},
				{
					"closed range",
					IncidentFilter{From: fixture.start, To: fixture.mid},
					fixture.ids("openHigh", "closedCritical"),
				},
				{
					"keyword with LIKE metacharacter is literal",
					IncidentFilter{Keyword: "%"},
					map[string]bool{},
				},
			}
			for _, testCase := range cases {
				incidents, _, err := repository.ListIncidents(context.Background(), fixture.tenantID, testCase.filter, ListOptions{})
				if err != nil {
					t.Fatalf("%s: %v", testCase.label, err)
				}
				got := incidentIDs(incidents)
				if len(got) != len(testCase.want) {
					t.Fatalf("%s: got %d incidents, want %d", testCase.label, len(got), len(testCase.want))
				}
				for id := range testCase.want {
					if !got[id] {
						t.Fatalf("%s: missing incident %s", testCase.label, id)
					}
				}
			}
		})
	}
}

// Adding predicates must not drop the tenant predicate that was already there:
// a filter matching another tenant's row has to return nothing.
func TestIncidentFilterKeepsTenantBoundary(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedIncidentFilterFixture(t, repository)
			otherTenant, otherAsset, otherBaseline := seedIncidentFixture(t, repository)
			if _, _, err := repository.UpsertIncident(context.Background(), incidentRound(
				otherTenant, otherAsset, otherBaseline, "fp-filter-other-tenant", domain.SeverityHigh,
				[]domain.Signal{signal("tamper")}, nil, time.Now().UTC())); err != nil {
				t.Fatal(err)
			}

			for _, filter := range []IncidentFilter{
				{Severity: "HIGH"},
				{Keyword: "篡改"},
				{AssetID: otherAsset},
			} {
				incidents, _, err := repository.ListIncidents(context.Background(), fixture.tenantID, filter, ListOptions{})
				if err != nil {
					t.Fatalf("%v: %v", filter, err)
				}
				for _, incident := range incidents {
					if incident.TenantID != fixture.tenantID {
						t.Fatalf("%v returned an incident from tenant %s", filter, incident.TenantID)
					}
				}
			}
		})
	}
}
