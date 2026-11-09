// Package authtest is for tests: it reads the permission (security scope) each operation declares in the contract.
// The permission table is written only once, in the contract; tests read it directly instead of keeping a copy.
package authtest

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// Op is one operation in the contract. Public = security: [] (no login required); otherwise Scopes holds the declared permissions,
// and is empty when none are declared (inheriting the global bearerAuth: []).
type Op struct {
	ID, Method, Path string
	Public           bool
	Scopes           []string
	re               *regexp.Regexp
	nParams          int
}

// Load reads the contract files (relative to the test's directory) and returns every operation.
func Load(t testing.TB, files ...string) []Op {
	t.Helper()
	var ops []Op
	for _, f := range files {
		l := openapi3.NewLoader()
		l.IsExternalRefsAllowed = true // enterprise.yaml references types from openapi.yaml
		doc, err := l.LoadFromFile(f)
		if err != nil {
			t.Fatalf("讀合約 %s: %v", f, err)
		}
		for p, item := range doc.Paths.Map() {
			for m, op := range item.Operations() {
				o := Op{ID: op.OperationID, Method: m, Path: p, nParams: strings.Count(p, "{"), re: pathRegexp(p)}
				switch {
				case op.Security != nil && len(*op.Security) == 0:
					o.Public = true
				case op.Security != nil && len(*op.Security) == 1:
					o.Scopes = (*op.Security)[0]["bearerAuth"]
				}
				ops = append(ops, o)
			}
		}
	}
	// literal paths first (/validator-votes/history before /validator-votes/{address}), matching chi's order
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].nParams < ops[j].nParams })
	return ops
}

// Find returns the operation that an actual method + path (including /api/v1) maps to.
func Find(ops []Op, method, path string) (Op, bool) {
	for _, o := range ops {
		if strings.EqualFold(o.Method, method) && o.re.MatchString(path) {
			return o, true
		}
	}
	return Op{}, false
}

// Example returns the actual path with every path parameter replaced by x, for tests that run once per operation.
func (o Op) Example() string {
	return "/api/v1" + paramRe.ReplaceAllString(o.Path, "x")
}

// Perm returns the permission declared for an actual method + path; if no operation matches or it does not declare exactly one, it records an error and returns "".
func Perm(t testing.TB, ops []Op, method, path string) string {
	t.Helper()
	o, ok := Find(ops, method, path)
	if !ok || len(o.Scopes) != 1 {
		t.Errorf("%s %s:合約裡找不到 operation 或沒宣告權限(found=%v)", method, path, ok)
		return ""
	}
	return o.Scopes[0]
}

var paramRe = regexp.MustCompile(`\{[^}]+\}`)

// pathRegexp turns a contract path template into a regexp matching actual paths (including /api/v1); each {param} matches one segment.
func pathRegexp(tmpl string) *regexp.Regexp {
	parts := paramRe.Split("/api/v1"+tmpl, -1)
	for i := range parts {
		parts[i] = regexp.QuoteMeta(parts[i])
	}
	return regexp.MustCompile("^" + strings.Join(parts, "[^/]+") + "$")
}
