package api

import (
	"testing"
	"time"
)

func trendDays(t *testing.T, trend []map[string]any) []string {
	t.Helper()
	days := make([]string, 0, len(trend))
	for _, entry := range trend {
		days = append(days, entry["date"].(string))
	}
	return days
}

func parseDay(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// TestBuildTrendFillsQuietDays: a chart that skipped the days with no incidents
// would draw a line straight from before a quiet stretch to after it, which
// reads as continuous activity across a period when nothing happened.
func TestBuildTrendFillsQuietDays(t *testing.T) {
	counts := map[string]map[string]int{
		"2026-09-01": {"HIGH": 2},
		"2026-09-04": {"CRITICAL": 1},
	}
	trend := buildTrend(counts,
		parseDay(t, "2026-09-01"), parseDay(t, "2026-09-04"))

	if len(trend) != 4 {
		t.Fatalf("got %d days, want 4 (the range is inclusive)", len(trend))
	}
	want := []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04"}
	for index, day := range trendDays(t, trend) {
		if day != want[index] {
			t.Fatalf("day %d = %s, want %s", index, day, want[index])
		}
	}
	// The quiet days are present and carry zero, not absent.
	for _, index := range []int{1, 2} {
		if trend[index]["total"].(int) != 0 {
			t.Fatalf("day %d total = %v, want 0", index, trend[index]["total"])
		}
	}
	if trend[0]["high"].(int) != 2 || trend[3]["critical"].(int) != 1 {
		t.Fatalf("the counts did not land on their days: %#v", trend)
	}
}

// TestBuildTrendAnchorsAnUnsetRangeToTheData: a report request need not carry a
// range, and treating an absent bound as the zero time would produce a series
// of nothing. The default window is the thirty days the overview chart shows.
func TestBuildTrendAnchorsAnUnsetRangeToTheData(t *testing.T) {
	counts := map[string]map[string]int{"2026-09-20": {"LOW": 1}}
	trend := buildTrend(counts, time.Time{}, time.Time{})

	if len(trend) != 30 {
		t.Fatalf("got %d days, want the 30-day default window", len(trend))
	}
	last := trendDays(t, trend)[len(trend)-1]
	if last != "2026-09-20" {
		t.Fatalf("the window ended on %s, want the newest day with data", last)
	}
	if trend[len(trend)-1]["low"].(int) != 1 {
		t.Fatalf("the count fell outside the anchored window: %#v", trend[len(trend)-1])
	}
}

func TestBuildTrendIsEmptyWithoutData(t *testing.T) {
	if trend := buildTrend(map[string]map[string]int{}, time.Time{}, time.Time{}); len(trend) != 0 {
		t.Fatalf("an empty tenant produced %d points", len(trend))
	}
}

// TestBuildTrendCapsTheWindow: the range comes from the caller, so an enormous
// one must not make the server allocate an unbounded slice.
func TestBuildTrendCapsTheWindow(t *testing.T) {
	counts := map[string]map[string]int{"2026-09-20": {"LOW": 1}}
	trend := buildTrend(counts,
		parseDay(t, "1900-01-01"), parseDay(t, "2026-09-20"))

	if len(trend) > 366 {
		t.Fatalf("the window was not capped: %d days", len(trend))
	}
	// The cap keeps the end, which is the part an operator is looking at.
	if last := trendDays(t, trend)[len(trend)-1]; last != "2026-09-20" {
		t.Fatalf("the cap dropped the newest day: ended on %s", last)
	}
}

// TestBuildTrendAlwaysCarriesEverySeverityKey: the chart reads a fixed set of
// keys, so a day must not omit the ones that happen to be zero -- a missing key
// becomes undefined in the series rather than a zero point.
func TestBuildTrendAlwaysCarriesEverySeverityKey(t *testing.T) {
	trend := buildTrend(
		map[string]map[string]int{"2026-09-20": {"CRITICAL": 1}},
		parseDay(t, "2026-09-20"), parseDay(t, "2026-09-20"))

	entry := trend[0]
	for _, key := range []string{"critical", "high", "medium", "low", "info", "total", "date"} {
		if _, ok := entry[key]; !ok {
			t.Fatalf("the trend entry is missing %q: %#v", key, entry)
		}
	}
}
