package detection

import (
	"testing"
	"time"

	"webmonitoring/internal/domain"
)

// TestMaintenanceScheduleMatching covers the two ways a weekly window is easy to
// get wrong: the day boundary, and the fact that the customer says "every
// Wednesday at 02:00" in their own clock rather than in UTC.
func TestMaintenanceScheduleMatching(t *testing.T) {
	// 2026-10-07 is a Wednesday.
	wednesday := func(hour, minute int) time.Time {
		return time.Date(2026, 10, 7, hour, minute, 0, 0, time.UTC)
	}
	cases := []struct {
		name     string
		schedule domain.MaintenanceSchedule
		at       time.Time
		want     bool
	}{
		{
			name:     "inside the window",
			schedule: domain.MaintenanceSchedule{Weekdays: []int{3}, StartMinute: 120, EndMinute: 240},
			at:       wednesday(3, 0),
			want:     true,
		},
		{
			name:     "after the window",
			schedule: domain.MaintenanceSchedule{Weekdays: []int{3}, StartMinute: 120, EndMinute: 240},
			at:       wednesday(5, 0),
			want:     false,
		},
		{
			name:     "the start is included",
			schedule: domain.MaintenanceSchedule{Weekdays: []int{3}, StartMinute: 120, EndMinute: 240},
			at:       wednesday(2, 0),
			want:     true,
		},
		{
			name:     "the end is excluded",
			schedule: domain.MaintenanceSchedule{Weekdays: []int{3}, StartMinute: 120, EndMinute: 240},
			at:       wednesday(4, 0),
			want:     false,
		},
		{
			name:     "the right time on the wrong day",
			schedule: domain.MaintenanceSchedule{Weekdays: []int{3}, StartMinute: 120, EndMinute: 240},
			at:       time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC),
			want:     false,
		},
		{
			name:     "no weekdays means every day",
			schedule: domain.MaintenanceSchedule{StartMinute: 120, EndMinute: 240},
			at:       time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC),
			want:     true,
		},
		{
			// A nightly freeze is the normal shape, and it has to match on both
			// sides of the day boundary or it never matches at all.
			name:     "crossing midnight, before it",
			schedule: domain.MaintenanceSchedule{StartMinute: 23 * 60, EndMinute: 60},
			at:       wednesday(23, 30),
			want:     true,
		},
		{
			name:     "crossing midnight, after it",
			schedule: domain.MaintenanceSchedule{StartMinute: 23 * 60, EndMinute: 60},
			at:       wednesday(0, 30),
			want:     true,
		},
		{
			name:     "crossing midnight, outside it",
			schedule: domain.MaintenanceSchedule{StartMinute: 23 * 60, EndMinute: 60},
			at:       wednesday(12, 0),
			want:     false,
		},
		{
			// Wednesday 23:00-01:00 covers Thursday 00:30 as well, because the
			// window is the one that started on Wednesday.
			name:     "crossing midnight belongs to the day it started",
			schedule: domain.MaintenanceSchedule{Weekdays: []int{3}, StartMinute: 23 * 60, EndMinute: 60},
			at:       time.Date(2026, 10, 8, 0, 30, 0, 0, time.UTC),
			want:     true,
		},
		{
			name:     "a crossing window does not run into the day before",
			schedule: domain.MaintenanceSchedule{Weekdays: []int{3}, StartMinute: 23 * 60, EndMinute: 60},
			at:       time.Date(2026, 10, 7, 0, 30, 0, 0, time.UTC),
			want:     false,
		},
		{
			// The customer means 02:00 on their own wall clock. UTC+8 02:00 is
			// 18:00 the previous day in UTC, which is the whole reason the offset
			// is stored rather than assumed.
			name:     "the offset moves the window",
			schedule: domain.MaintenanceSchedule{Weekdays: []int{3}, StartMinute: 120, EndMinute: 240, UTCOffsetMinutes: 480},
			// 02:30 on Wednesday in UTC+8 is 18:30 on Tuesday in UTC: the window
			// has to be found a day early, which is the whole point of storing
			// the offset rather than assuming UTC.
			at:   time.Date(2026, 10, 6, 18, 30, 0, 0, time.UTC),
			want: true,
		},
		{
			name:     "an offset window does not match the naive hour",
			schedule: domain.MaintenanceSchedule{Weekdays: []int{3}, StartMinute: 120, EndMinute: 240, UTCOffsetMinutes: 480},
			at:       wednesday(3, 0),
			want:     false,
		},
		{
			name:     "an empty window never matches",
			schedule: domain.MaintenanceSchedule{StartMinute: 0, EndMinute: 0},
			at:       wednesday(0, 0),
			want:     false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.schedule.Contains(testCase.at); got != testCase.want {
				t.Fatalf("Contains(%s) = %v, want %v", testCase.at.Format(time.RFC3339), got, testCase.want)
			}
		})
	}
}

// The one-off range and the weekly recurrence are additive, so an asset can
// carry both without one cancelling the other.
func TestMaintenanceWindowCombinesBothForms(t *testing.T) {
	at := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	other := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)

	recurring := domain.MaintenanceSchedule{Weekdays: []int{3}, StartMinute: 120, EndMinute: 240}
	from := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)

	both := domain.MonitorPolicy{MaintenanceWindowStart: &from, MaintenanceWindowEnd: &to, MaintenanceSchedule: &recurring}
	if !inMaintenanceWindow(both, at) {
		t.Fatal("the recurrence was ignored when an absolute range was also set")
	}
	if !inMaintenanceWindow(both, other) {
		t.Fatal("the absolute range was ignored when a recurrence was also set")
	}
	if inMaintenanceWindow(both, time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("neither form covers noon, but the window matched")
	}
}
