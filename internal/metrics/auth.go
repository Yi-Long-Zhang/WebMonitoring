package metrics

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuthorized reports whether request carries the expected bearer token.
// An empty expected token keeps the endpoint open -- the development default,
// which production validation refuses to start on. The comparison is constant
// time so an unauthenticated caller cannot measure their way to the token.
func BearerAuthorized(request *http.Request, expected string) bool {
	if expected == "" {
		return true
	}
	token := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}
