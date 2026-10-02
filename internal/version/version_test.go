package version

import "testing"

// A binary built without link-time injection must still answer with something
// that is true. An empty version reads as a broken endpoint, and the operator
// then spends the incident wondering whether the deployment is what they think
// it is.
func TestCurrentNeverReportsEmptyFields(t *testing.T) {
	info := Current()
	if info.Version == "" || info.Commit == "" || info.BuildTime == "" || info.GoVersion == "" {
		t.Fatalf("Current() left a field empty: %+v", info)
	}
}
