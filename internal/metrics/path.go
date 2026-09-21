package metrics

import (
	"path"
	"strings"
)

// PathClass turns a request path into a low-cardinality label value for the
// HTTP metrics.
//
// The request path cannot be used as a label directly. Every asset, incident,
// evidence item and tenant id is its own path -- and a signed evidence token is
// a credential, so exporting raw paths would make a metric label a secret. The
// classifier therefore reports the resource collection a request names, not the
// item it names: /api/v1/assets/<id> classes as /api/v1/assets.
//
// It is deliberately not derived from the router. The route pattern is not
// reachable from the middleware, and a class list that lives here is testable
// without standing up a server.
func PathClass(requestPath string) string {
	clean := path.Clean("/" + strings.TrimPrefix(requestPath, "/"))
	segments := strings.Split(strings.Trim(clean, "/"), "/")

	switch segments[0] {
	case "healthz", "readyz", "metrics":
		return "/" + segments[0]
	case "api":
		// Anything outside the known surface still classes, as the prefix it
		// shares -- so a probe hammering an unknown path lands on its own bucket
		// rather than disappearing from the metrics entirely.
		if len(segments) < 3 || segments[1] != "v1" {
			return fallbackClass(clean)
		}
		if segments[2] == "evidence" && len(segments) >= 4 && segments[3] == "signed" {
			// The token is a bearer credential and must never become a label.
			return "/api/v1/evidence/download"
		}
		return "/api/v1/" + segments[2]
	default:
		return fallbackClass(clean)
	}
}

// fallbackClass bounds the cardinality of a path the classifier does not know.
// Keeping the first two segments lets a new collection be spotted in the
// metrics, while a crawler walking arbitrary depth cannot mint unbounded keys.
func fallbackClass(clean string) string {
	segments := strings.Split(strings.Trim(clean, "/"), "/")
	if len(segments) > 2 {
		segments = segments[:2]
	}
	return "/" + strings.Join(segments, "/")
}
