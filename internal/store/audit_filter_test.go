package store

import (
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

// auditFixture is the shape every assertion below reads: one tenant with a
// known set of audit rows, plus a second tenant holding a row that matches the
// first tenant's filter values. That second row is what proves the tenant
// predicate is not lost when a filter is added.
type auditFixture struct {
	tenantID     string
	otherID      string
	actor        string
	otherActor   string
	foreign      string
	base         time.Time
	byLabel      map[string]string
	allIDs       map[string]bool
	actionIDs    map[string]bool
	actorIDs     map[string]bool
	resourceIDs  map[string]bool
	failedIDs    map[string]bool
	automatedIDs map[string]bool
}

func seedAuditFixture(t *testing.T, repository Repository) auditFixture {
	t.Helper()
	suffix := identity.New()
	now := time.Now().UTC().Truncate(time.Second)
	fixture := auditFixture{
		base:         now,
		byLabel:      map[string]string{},
		allIDs:       map[string]bool{},
		actionIDs:    map[string]bool{},
		actorIDs:     map[string]bool{},
		resourceIDs:  map[string]bool{},
		failedIDs:    map[string]bool{},
		automatedIDs: map[string]bool{},
	}
	fixture.tenantID = seedAuditTenant(t, repository, "audit-filter-"+suffix, "admin-"+suffix)
	fixture.otherID = seedAuditTenant(t, repository, "audit-other-"+suffix, "other-"+suffix)
	fixture.actor = auditUserID(t, repository, fixture.tenantID)
	fixture.otherActor = auditUserID(t, repository, fixture.otherID)

	// audit_events.id is a uuid and AppendAudit binds it without a cast, so the
	// fixture must use real uuids: a readable literal like "a1" is accepted by
	// the memory store and rejected by Postgres with 22P02. Labels map to the
	// generated ids so the assertions below stay readable.
	type seed struct {
		label    string
		action   string
		resource string
		result   string
		actor    string // empty means an automated write with no actor
		offset   time.Duration
	}
	seeds := []seed{
		{label: "create1", action: "asset.create", resource: "asset-1", result: "SUCCESS", actor: fixture.actor, offset: 0},
		{label: "create2", action: "asset.create", resource: "asset-2", result: "SUCCESS", actor: fixture.actor, offset: -time.Minute},
		{label: "update1", action: "asset.update", resource: "asset-1", result: "SUCCESS", actor: fixture.actor, offset: -2 * time.Minute},
		{label: "tenantUpdate", action: "tenant.update", resource: "tenant-1", result: "FAILED", actor: fixture.actor, offset: -3 * time.Minute},
		{label: "baseline", action: "baseline.approved", resource: "baseline-1", result: "SUCCESS", actor: fixture.actor, offset: -4 * time.Minute},
		// Automated writes carry no actor. A filter on actorId must never match
		// these, in either repository.
		{label: "retention", action: "maintenance.retention.scheduled", resource: "retention", result: "PARTIAL", offset: -5 * time.Minute},
		{label: "deadLetter", action: "alert.dead_letter", resource: "alert-1", result: "FAILED", offset: -6 * time.Minute},
		// A second asset.update in the same tenant, so resource filtering is not
		// accidentally equivalent to action filtering.
		{label: "update2", action: "asset.update", resource: "asset-1", result: "SUCCESS", actor: fixture.actor, offset: -7 * time.Minute},
	}
	for _, row := range seeds {
		id := identity.New()
		fixture.byLabel[row.label] = id
		event := domain.AuditEvent{
			ID: id, TenantID: fixture.tenantID, ActorID: row.actor,
			Action: row.action, ResourceType: "asset", ResourceID: row.resource,
			Result: row.result, TraceID: "trace-" + row.label,
			CreatedAt: fixture.base.Add(row.offset),
		}
		if err := repository.AppendAudit(event); err != nil {
			t.Fatalf("append audit %s: %v", row.label, err)
		}
		fixture.allIDs[id] = true
		switch row.action {
		case "asset.create", "asset.update":
			fixture.actionIDs[id] = true
		}
		if row.actor == fixture.actor {
			fixture.actorIDs[id] = true
		}
		if row.resource == "asset-1" {
			fixture.resourceIDs[id] = true
		}
		if row.result == "FAILED" {
			fixture.failedIDs[id] = true
		}
		if row.actor == "" {
			fixture.automatedIDs[id] = true
		}
	}
	// The other tenant holds rows that match the first tenant's filter values,
	// so a filter that drops the tenant predicate cannot pass unnoticed.
	fixture.foreign = identity.New()
	if err := repository.AppendAudit(domain.AuditEvent{
		ID: fixture.foreign, TenantID: fixture.otherID, ActorID: fixture.otherActor,
		Action: "asset.create", ResourceType: "asset", ResourceID: "asset-1",
		Result: "SUCCESS", TraceID: "trace-foreign", CreatedAt: fixture.base,
	}); err != nil {
		t.Fatalf("append foreign audit: %v", err)
	}
	return fixture
}

// ids resolves fixture labels to the generated uuids.
func (f auditFixture) ids(labels ...string) map[string]bool {
	resolved := make(map[string]bool, len(labels))
	for _, label := range labels {
		id, ok := f.byLabel[label]
		if !ok {
			panic("unknown fixture label " + label)
		}
		resolved[id] = true
	}
	return resolved
}

func seedAuditTenant(t *testing.T, repository Repository, slug, username string) string {
	t.Helper()
	now := time.Now().UTC()
	tenant := domain.Tenant{ID: identity.New(), Slug: slug, Name: slug, CreatedAt: now}
	if err := repository.CreateTenant(tenant); err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	if err := repository.CreateUser(domain.User{
		ID: identity.New(), TenantID: tenant.ID, Username: username,
		PasswordHash: "hash", Role: domain.RoleAdmin, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return tenant.ID
}

func auditUserID(t *testing.T, repository Repository, tenantID string) string {
	t.Helper()
	users, err := repository.ListUsers(tenantID)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) == 0 {
		t.Fatal("fixture tenant has no user")
	}
	return users[0].ID
}

func auditIDs(events []domain.AuditEvent) map[string]bool {
	ids := make(map[string]bool, len(events))
	for _, event := range events {
		ids[event.ID] = true
	}
	return ids
}

func assertSameIDSet(t *testing.T, label string, got map[string]bool, want map[string]bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d rows %v, want %d %v", label, len(got), got, len(want), want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("%s: missing %s in %v", label, id, got)
		}
	}
}

// Every filter dimension must select the same rows in both repositories. The
// in-memory store implements the predicates in Go and Postgres implements them
// in SQL, so nothing but this test keeps the two definitions aligned.
func TestAuditFilterAgreesAcrossStores(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedAuditFixture(t, repository)

			cases := []struct {
				label  string
				filter AuditFilter
				want   map[string]bool
			}{
				{"no filter", AuditFilter{}, fixture.allIDs},
				{"actor", AuditFilter{ActorID: fixture.actor}, fixture.actorIDs},
				{"action asset.create", AuditFilter{Action: "asset.create"}, fixture.ids("create1", "create2")},
				{"action asset.update", AuditFilter{Action: "asset.update"}, fixture.ids("update1", "update2")},
				{"resource asset-1", AuditFilter{ResourceID: "asset-1"}, fixture.resourceIDs},
				{"result FAILED", AuditFilter{Result: "FAILED"}, fixture.failedIDs},
				{
					"action+result",
					AuditFilter{Action: "asset.create", Result: "SUCCESS"},
					fixture.ids("create1", "create2"),
				},
				{
					"actor+resource",
					AuditFilter{ActorID: fixture.actor, ResourceID: "asset-1"},
					fixture.resourceIDs,
				},
			}
			for _, testCase := range cases {
				events, _, err := repository.ListAudits(fixture.tenantID, testCase.filter, ListOptions{})
				if err != nil {
					t.Fatalf("%s: %v", testCase.label, err)
				}
				assertSameIDSet(t, testCase.label, auditIDs(events), testCase.want)
			}
		})
	}
}

// A filter that matches another tenant's row must return nothing: adding a
// predicate must not drop the tenant predicate that was already there.
func TestAuditFilterKeepsTenantBoundary(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedAuditFixture(t, repository)

			// `asset.create` and `asset-1` both exist in the other tenant too.
			for _, filter := range []AuditFilter{
				{Action: "asset.create", ResourceID: "asset-1"},
				{ActorID: fixture.otherActor},
			} {
				events, _, err := repository.ListAudits(fixture.tenantID, filter, ListOptions{})
				if err != nil {
					t.Fatalf("%v: %v", filter, err)
				}
				for _, event := range events {
					if event.TenantID != fixture.tenantID {
						t.Fatalf("filter %v leaked tenant %s", filter, event.TenantID)
					}
					if event.ID == fixture.foreign {
						t.Fatalf("filter %v returned the foreign row", filter)
					}
				}
			}
		})
	}
}

// A filtered walk must apply the filter inside the page, not to a page taken
// from the unfiltered ordering: otherwise the cursor advances past rows the
// caller asked for and the walk silently loses them.
func TestAuditFilterSurvivesCursorWalk(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedAuditFixture(t, repository)
			filter := AuditFilter{ActorID: fixture.actor}

			seen := map[string]bool{}
			cursor := ""
			for pages := 0; pages < 20; pages++ {
				events, next, err := repository.ListAudits(fixture.tenantID, filter, ListOptions{Limit: 2, Cursor: cursor})
				if err != nil {
					t.Fatalf("page %d: %v", pages, err)
				}
				for _, event := range events {
					if seen[event.ID] {
						t.Fatalf("id %s returned twice during the walk", event.ID)
					}
					if event.ActorID != fixture.actor {
						t.Fatalf("walk returned %s whose actor is %q", event.ID, event.ActorID)
					}
					seen[event.ID] = true
				}
				if next == "" {
					break
				}
				cursor = next
			}
			assertSameIDSet(t, "filtered walk", seen, fixture.actorIDs)
		})
	}
}

// A time range is inclusive on both ends, matching the SQL bounds.
func TestAuditFilterTimeRangeIsInclusive(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedAuditFixture(t, repository)

			// a2 and a3 sit at -1m and -2m; both ends included.
			events, _, err := repository.ListAudits(fixture.tenantID, AuditFilter{
				From: fixture.base.Add(-2 * time.Minute),
				To:   fixture.base.Add(-1 * time.Minute),
			}, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			assertSameIDSet(t, "inclusive range", auditIDs(events), fixture.ids("create2", "update1"))

			// A one-sided bound is unbounded on the other side.
			events, _, err = repository.ListAudits(fixture.tenantID, AuditFilter{
				From: fixture.base.Add(-time.Minute),
			}, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			assertSameIDSet(t, "open upper bound", auditIDs(events), fixture.ids("create1", "create2"))
		})
	}
}

// Automated writes (the retention sweep, dead-letter handlers) are stored with
// a NULL actor_id. No actorId filter may ever match them, and they must stay
// reachable when no actor filter is set -- both properties have to hold in
// both repositories, because Postgres stores NULL where memory stores "".
func TestAuditFilterNeverMatchesAutomatedWrites(t *testing.T) {
	for name, repository := range testRepositories(t) {
		t.Run(name, func(t *testing.T) {
			fixture := seedAuditFixture(t, repository)

			filtered, _, err := repository.ListAudits(fixture.tenantID, AuditFilter{ActorID: fixture.actor}, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range filtered {
				if fixture.automatedIDs[event.ID] {
					t.Fatalf("actor filter returned automated row %s", event.ID)
				}
			}

			unfiltered, _, err := repository.ListAudits(fixture.tenantID, AuditFilter{}, ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			seen := auditIDs(unfiltered)
			for id := range fixture.automatedIDs {
				if !seen[id] {
					t.Fatalf("automated row %s unreachable without an actor filter", id)
				}
			}
		})
	}
}
