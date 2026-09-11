package detection

import "testing"

func TestAnalyzeHTMLIgnoresDynamicRegionAndExtractsSignals(t *testing.T) {
	raw := []byte(`
		<html><head><title>Portal</title><script src="/app.js"></script></head>
		<body>
			<div id="clock">2026-09-11 12:00:00</div>
			<a href="https://outside.example/path">outside</a>
			<iframe src="/frame"></iframe>
			<form action="/login" method="post"><input name="username"></form>
			<p>Trusted body</p>
		</body></html>`)
	analysis, err := AnalyzeHTML(raw, "https://example.com/", []string{"#clock"})
	if err != nil {
		t.Fatalf("analyze html: %v", err)
	}
	if analysis.Title != "Portal" {
		t.Fatalf("unexpected title: %q", analysis.Title)
	}
	if analysis.VisibleText != "outside Trusted body" {
		t.Fatalf("dynamic region should be omitted, got %q", analysis.VisibleText)
	}
	if len(analysis.Scripts) != 1 || analysis.Scripts[0].URL != "https://example.com/app.js" {
		t.Fatalf("unexpected scripts: %#v", analysis.Scripts)
	}
	if len(analysis.Forms) != 1 || analysis.Forms[0].Action != "https://example.com/login" {
		t.Fatalf("unexpected forms: %#v", analysis.Forms)
	}
}
