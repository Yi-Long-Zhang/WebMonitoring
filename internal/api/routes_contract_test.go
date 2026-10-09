package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The contract is checked in exactly one direction today: CI regenerates the
// console's TypeScript types from api/openapi.yaml and diffs them
// (.github/workflows/ci.yml), which catches "the spec changed and the types were
// not regenerated". Nothing catches the other direction -- a handler registered
// in server.go whose operation was never written into the contract -- and in
// that case every gate stays green while the contract quietly stops describing
// the API. This test is that direction.
//
// It reads source rather than running the server on purpose. routes() builds two
// ServeMux values and mounts the protected one behind an auth middleware
// (server.go:291); that mux is a local variable, so nothing at runtime can
// enumerate it -- (*http.ServeMux).Patterns() on the returned handler would show
// the five root registrations and the mount, and nothing else.
//
// It also needs no test server fixture (no userMgmtServer/retentionServer/
// doJSON): it opens two files. Reusing those helpers would build a repository
// and a temp evidence store to answer a question about text.

const apiPrefix = "/api/v1"

// httpMethods is the vocabulary a registration may use. It doubles as the
// contract-side whitelist: a path item's `parameters:` key sits at the same
// 4-space indent as its methods, so the parser has to match method names
// explicitly rather than "any key at that depth".
var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "HEAD": true, "OPTIONS": true, "TRACE": true,
}

var (
	pathKeyRe = regexp.MustCompile(`^  (/\S*):\s*(#.*)?$`)
	methodRe  = regexp.MustCompile(`^    (get|post|put|patch|delete|head|options|trace):`)
	topKeyRe  = regexp.MustCompile(`^[A-Za-z]`)
	serverRe  = regexp.MustCompile(`^\s*-\s+url:\s*(\S+)\s*$`)
)

// normalizePath strips the base path the contract declares in `servers:` so both
// sides can be compared as the contract writes them. The literal patterns in
// server.go carry the full path even when they are registered behind the
// root.Handle("/api/v1/", ...) mount, and the three endpoints served at the root
// (/healthz, /readyz, /metrics) do not carry it -- stripping normalizes both
// without a list of exceptions.
//
// The prefix is anchored on the whole segment: "/api/v10/foo" is not under
// "/api/v1" and passes through unchanged, so if such a route is ever added it
// shows up as a drift instead of silently matching.
func normalizePath(path string) string {
	switch {
	case path == apiPrefix:
		return "/"
	case strings.HasPrefix(path, apiPrefix+"/"):
		return strings.TrimPrefix(path, apiPrefix)
	default:
		return path
	}
}

// registeredOperations parses server.go and returns every "METHOD /path" that
// routes() registers, plus whatever prefix mounts it makes.
//
// AST rather than a text scan: the body of routes() is full of comments that
// spell out route strings (server.go:214-252), and a scanner would collect those
// as if they were registrations. A commented-out registration would likewise be
// counted by a scanner and is correctly invisible here -- it is not registered.
func registeredOperations(t *testing.T) (operations, mounts map[string]struct{}) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "server.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	// Failure messages carry a line number: this gate is only read when it fails,
	// and "somewhere in server.go" is not actionable.
	line := func(pos token.Pos) int { return fset.Position(pos).Line }

	var routesFn *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "routes" || fn.Recv == nil || fn.Body == nil {
			continue
		}
		routesFn = fn
		break
	}
	if routesFn == nil {
		t.Fatal("server.go: (*Server).routes not found -- the gate cannot enumerate routes")
	}

	operations, mounts = map[string]struct{}{}, map[string]struct{}{}
	calls := 0
	ast.Inspect(routesFn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (selector.Sel.Name != "Handle" && selector.Sel.Name != "HandleFunc") {
			return true
		}
		calls++
		if len(call.Args) == 0 {
			t.Fatalf("server.go:%d: %s called with no pattern", line(call.Pos()), selector.Sel.Name)
		}

		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			t.Fatalf("server.go:%d: route pattern is not a string literal; "+
				"this gate only understands literal patterns, and a computed one has to be handled deliberately",
				line(call.Args[0].Pos()))
		}
		pattern, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatalf("server.go:%d: unquote %s: %v", line(literal.Pos()), literal.Value, err)
		}

		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			// A pattern without a method is a prefix mount, not an operation.
			// Recorded rather than skipped so a second mount is caught instead
			// of silently dropped.
			if !strings.HasSuffix(pattern, "/") {
				t.Fatalf("server.go:%d: pattern %q is neither \"METHOD /path\" nor a prefix mount",
					line(literal.Pos()), pattern)
			}
			mounts[pattern] = struct{}{}
			return true
		}
		if !httpMethods[method] {
			t.Fatalf("server.go:%d: unknown HTTP method %q in %q", line(literal.Pos()), method, pattern)
		}

		key := method + " " + normalizePath(path)
		if _, duplicate := operations[key]; duplicate {
			t.Fatalf("server.go:%d: %s registered more than once", line(literal.Pos()), key)
		}
		operations[key] = struct{}{}
		return true
	})

	if calls == 0 {
		t.Fatal("server.go: routes() contains no Handle/HandleFunc calls -- " +
			"either the registrations moved out of it or this gate stopped reading the right function")
	}
	return operations, mounts
}

// contractOperations parses api/openapi.yaml by line, which is what the file's
// shape allows: path keys at 2 spaces, method keys at 4, and no YAML library in
// go.mod to parse it properly (adding one is not possible on this network).
// The self-checks in the test body are what keep a line parser honest.
func contractOperations(t *testing.T) (operations map[string]struct{}, paths int, serverURL string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}

	operations = map[string]struct{}{}
	inPaths := false
	current := ""
	for _, line := range strings.Split(string(raw), "\n") {
		// This repository is checked out with CRLF on Windows, and an
		// end-anchored pattern would silently stop matching.
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if !inPaths {
			if line == "paths:" {
				inPaths = true
				continue
			}
			if match := serverRe.FindStringSubmatch(line); match != nil {
				serverURL = match[1]
			}
			continue
		}
		// A key at column 0 after `paths:` ends the section (components:).
		if topKeyRe.MatchString(line) {
			break
		}
		if match := pathKeyRe.FindStringSubmatch(line); match != nil {
			current = match[1]
			paths++
			continue
		}
		// Deliberately no `$` after the colon: `get: { ... }` is legal YAML and
		// an end anchor would drop that operation silently.
		if match := methodRe.FindStringSubmatch(line); match != nil && current != "" {
			key := strings.ToUpper(match[1]) + " " + current
			if _, duplicate := operations[key]; duplicate {
				t.Fatalf("api/openapi.yaml: %s declared twice", key)
			}
			operations[key] = struct{}{}
		}
	}
	return operations, paths, serverURL
}

func TestRoutesMatchOpenAPIContract(t *testing.T) {
	registered, mounts := registeredOperations(t)
	declared, paths, serverURL := contractOperations(t)

	// The bounds are what make a broken parser red instead of green: two empty
	// sets compare equal, and a gate that cannot fail is worse than no gate.
	if len(registered) < 60 {
		t.Fatalf("enumerated %d registered operations from server.go, expected at least 60", len(registered))
	}
	if len(declared) < 60 || paths < 50 {
		t.Fatalf("parsed %d operations across %d paths from api/openapi.yaml, expected at least 60 across 50", len(declared), paths)
	}
	if serverURL != apiPrefix {
		t.Fatalf("api/openapi.yaml declares servers url %q; this gate normalizes on %q -- update the normalization if the base path moved",
			serverURL, apiPrefix)
	}
	if _, ok := mounts["/api/v1/"]; !ok || len(mounts) != 1 {
		t.Fatalf("server.go prefix mounts are %v, expected exactly the \"/api/v1/\" mount", sortedKeys(mounts))
	}
	for key := range registered {
		assertOperationShape(t, key, "server.go")
	}
	for key := range declared {
		assertOperationShape(t, key, "api/openapi.yaml")
	}

	missing := difference(declared, registered)
	extra := difference(registered, declared)
	t.Logf("compared %d registered operations against %d declared across %d paths", len(registered), len(declared), paths)

	if len(missing) > 0 || len(extra) > 0 || len(registered) != len(declared) {
		t.Errorf("routes and api/openapi.yaml disagree: %d registered in internal/api/server.go, %d declared in api/openapi.yaml\n"+
			"registered but missing from api/openapi.yaml -- add them to the contract, then regenerate the client types with "+
			"`pnpm --filter @webmonitoring/console generate:api`:\n%s\n"+
			"declared but not registered -- either the registration was dropped, or the operation was retired on purpose; "+
			"if it was retired, delete the path from api/openapi.yaml and regenerate the client types:\n%s",
			len(registered), len(declared), formatList(missing), formatList(extra))
	}
}

// assertOperationShape rejects a key that is not "UPPERCASE-METHOD /path". A
// parser that captured the wrong thing (a path-level `parameters:`, a stray
// line) produces such a key, and it is better to stop on it than to carry it
// into the comparison where it reads as a drift in the contract.
func assertOperationShape(t *testing.T, key, source string) {
	t.Helper()
	method, path, ok := strings.Cut(key, " ")
	if !ok || !httpMethods[method] {
		t.Fatalf("%s: parsed %q, which is not \"METHOD /path\"", source, key)
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \t") {
		t.Fatalf("%s: parsed path %q, which is not a path", source, path)
	}
}

func difference(from, subtract map[string]struct{}) []string {
	out := []string{}
	for key := range from {
		if _, ok := subtract[key]; !ok {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(set map[string]struct{}) []string {
	out := []string{}
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func formatList(items []string) string {
	if len(items) == 0 {
		return "    (none)"
	}
	var builder strings.Builder
	for _, item := range items {
		builder.WriteString("    ")
		builder.WriteString(item)
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}
