package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"webmonitoring/internal/domain"
	"webmonitoring/internal/identity"
)

// policyServer returns a server holding one asset with a policy, which is what a
// recurring window is attached to.
func policyServer(t *testing.T) (*Server, *http.Cookie, string, string) {
	t.Helper()
	server, cookie, csrf, assetID, _ := escalationServer(t, domain.SeverityLow, domain.IncidentOpen, "", time.Second)
	if err := server.repository.UpsertPolicy(context.Background(), domain.MonitorPolicy{
		ID: identity.New(), TenantID: defaultTenant, AssetID: assetID, Version: 1,
		HTTPIntervalSeconds: 300, BrowserIntervalSeconds: 1800, TimeoutSeconds: 30,
		MaxResponseBytes: 1024 * 1024, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	return server, cookie, csrf, assetID
}

func putPolicy(t *testing.T, server *Server, cookie *http.Cookie, csrf, assetID, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/api/v1/policies/"+assetID, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func basePolicyBody(schedule string) string {
	body := `{"httpIntervalSeconds":300,"browserIntervalSeconds":1800,"timeoutSeconds":30,` +
		`"maxResponseBytes":1048576,"ignoreSelectors":[],"trustedDomains":[]`
	if schedule != "" {
		body += `,"maintenanceSchedule":` + schedule
	}
	return body + `}`
}

// A window that never matches stops monitoring without saying so: the asset
// looks quiet and healthy while nothing is being checked. That is why a
// malformed recurrence is refused rather than clamped.
func TestPolicyScheduleRejectsValuesThatWouldSilentlyNotWork(t *testing.T) {
	server, cookie, csrf, assetID := policyServer(t)
	cases := []struct {
		name     string
		schedule string
	}{
		{"start equals end", `{"startMinute":120,"endMinute":120}`},
		{"start beyond the day", `{"startMinute":1441,"endMinute":240}`},
		{"end below zero", `{"startMinute":120,"endMinute":-1}`},
		{"start missing", `{"endMinute":240}`},
		{"end missing", `{"startMinute":120}`},
		{"weekday out of range", `{"startMinute":120,"endMinute":240,"weekdays":[7]}`},
		{"negative weekday", `{"startMinute":120,"endMinute":240,"weekdays":[-1]}`},
		{"offset beyond the calendar", `{"startMinute":120,"endMinute":240,"utcOffsetMinutes":900}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := putPolicy(t, server, cookie, csrf, assetID, basePolicyBody(testCase.schedule))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != "INVALID_POLICY" {
				t.Fatalf("code=%q body=%s", body.Code, response.Body.String())
			}
		})
	}
}

// A window that is stored has to come back exactly, including the parts the
// scheduler interprets: dropping the offset would move the window by hours, and
// dropping the weekdays would turn a weekly freeze into a nightly one.
func TestPolicyScheduleRoundTrips(t *testing.T) {
	server, cookie, csrf, assetID := policyServer(t)
	response := putPolicy(t, server, cookie, csrf, assetID,
		basePolicyBody(`{"weekdays":[5,3,3],"startMinute":1380,"endMinute":120,"utcOffsetMinutes":480}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var saved domain.MonitorPolicy
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	schedule := saved.MaintenanceSchedule
	if schedule == nil {
		t.Fatal("the recurrence was not stored")
	}
	if schedule.StartMinute != 1380 || schedule.EndMinute != 120 || schedule.UTCOffsetMinutes != 480 {
		t.Fatalf("schedule = %+v, want the values that were sent", schedule)
	}
	// Sorted and de-duplicated on the way in, so the stored value does not depend
	// on the order a multi-select happened to submit.
	if len(schedule.Weekdays) != 2 || schedule.Weekdays[0] != 3 || schedule.Weekdays[1] != 5 {
		t.Fatalf("weekdays = %v, want [3 5]", schedule.Weekdays)
	}
}

// An explicit null clears the recurrence. Without that, a window could be added
// but never removed.
func TestPolicyScheduleCanBeCleared(t *testing.T) {
	server, cookie, csrf, assetID := policyServer(t)
	if response := putPolicy(t, server, cookie, csrf, assetID,
		basePolicyBody(`{"startMinute":120,"endMinute":240}`)); response.Code != http.StatusOK {
		t.Fatalf("seeding the recurrence failed: %d %s", response.Code, response.Body.String())
	}
	response := putPolicy(t, server, cookie, csrf, assetID, basePolicyBody(`null`))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var saved domain.MonitorPolicy
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.MaintenanceSchedule != nil {
		t.Fatalf("the recurrence survived an explicit null: %+v", saved.MaintenanceSchedule)
	}
}

// Omitting the key leaves the recurrence alone: the console edits a whole form,
// but an API client that only changes the interval must not silently drop the
// window the asset depends on.
func TestPolicyScheduleSurvivesAnUnrelatedUpdate(t *testing.T) {
	server, cookie, csrf, assetID := policyServer(t)
	if response := putPolicy(t, server, cookie, csrf, assetID,
		basePolicyBody(`{"startMinute":120,"endMinute":240}`)); response.Code != http.StatusOK {
		t.Fatalf("seeding the recurrence failed: %d %s", response.Code, response.Body.String())
	}
	response := putPolicy(t, server, cookie, csrf, assetID, basePolicyBody(""))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var saved domain.MonitorPolicy
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.MaintenanceSchedule == nil {
		t.Fatal("an update that did not mention the recurrence dropped it")
	}
}
