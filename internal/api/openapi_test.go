package api_test

import (
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"domieface/com/internal/api"
	"domieface/com/internal/httpx"
)

// openAPIDoc is the subset of the spec these tests reason about. It is
// deliberately partial: the point is to check the spec against the running
// server, not to reimplement an OpenAPI validator.
type openAPIDoc struct {
	OpenAPI string `yaml:"openapi"`
	Info    struct {
		Title   string `yaml:"title"`
		Version string `yaml:"version"`
	} `yaml:"info"`
	Servers []struct {
		URL string `yaml:"url"`
	} `yaml:"servers"`
	Security   []map[string][]string           `yaml:"security"`
	Paths      map[string]map[string]openAPIOp `yaml:"paths"`
	Components struct {
		SecuritySchemes map[string]yaml.Node `yaml:"securitySchemes"`
		Schemas         map[string]yaml.Node `yaml:"schemas"`
	} `yaml:"components"`
}

type openAPIOp struct {
	OperationID string               `yaml:"operationId"`
	Summary     string               `yaml:"summary"`
	Tags        []string             `yaml:"tags"`
	Responses   map[string]yaml.Node `yaml:"responses"`

	// Security is a pointer so an absent key (inherits the global requirement,
	// therefore protected) is distinguishable from an explicit empty list
	// (public).
	Security *[]map[string][]string `yaml:"security"`
}

// httpMethods are the keys under a path that describe an operation. Anything
// else at that level (a shared `parameters` list, say) is not one.
var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func loadSpec(t *testing.T) *openAPIDoc {
	t.Helper()

	var doc openAPIDoc
	if err := yaml.Unmarshal(api.OpenAPISpec, &doc); err != nil {
		t.Fatalf("the embedded spec is not valid YAML: %v", err)
	}
	return &doc
}

// specOperations returns every documented operation keyed as "METHOD /path".
func specOperations(t *testing.T, doc *openAPIDoc) map[string]openAPIOp {
	t.Helper()

	operations := make(map[string]openAPIOp)
	for path, item := range doc.Paths {
		for method, op := range item {
			if !slices.Contains(httpMethods, method) {
				continue
			}
			operations[strings.ToUpper(method)+" "+path] = op
		}
	}
	return operations
}

// routeKeys returns the server's actual routes in the same "METHOD /path" form.
func routeKeys(routes []api.Route) map[string]api.Route {
	keyed := make(map[string]api.Route, len(routes))
	for _, route := range routes {
		keyed[route.Method+" "+route.Pattern] = route
	}
	return keyed
}

// documentationRoutes describe the API rather than being part of it, so they are
// deliberately absent from the spec.
var documentationRoutes = []string{
	http.MethodGet + " " + api.DocsPath,
	http.MethodGet + " " + api.OpenAPIPath,
}

// This is the test that stops the spec rotting. A hand-written spec is only
// worth having if adding an undocumented route, or documenting one that does
// not exist, fails the build.
func TestOpenAPIMatchesRoutingTable(t *testing.T) {
	e := newEnv(t)
	doc := loadSpec(t)

	documented := specOperations(t, doc)
	served := routeKeys(e.api.Routes())

	for key := range served {
		if slices.Contains(documentationRoutes, key) {
			continue
		}
		if _, ok := documented[key]; !ok {
			t.Errorf("%s is served but not documented in openapi.yaml", key)
		}
	}

	for key := range documented {
		if _, ok := served[key]; !ok {
			t.Errorf("%s is documented in openapi.yaml but not served", key)
		}
	}
}

// A route the spec calls public but that actually demands a token (or the
// reverse) is worse than no documentation, because the client trusts it.
func TestOpenAPISecurityMatchesTheRoutingTable(t *testing.T) {
	e := newEnv(t)
	doc := loadSpec(t)

	if len(doc.Security) == 0 {
		t.Fatal("the spec declares no global security requirement, so every operation would read as public")
	}

	documented := specOperations(t, doc)
	for key, route := range routeKeys(e.api.Routes()) {
		if slices.Contains(documentationRoutes, key) {
			continue
		}
		op, ok := documented[key]
		if !ok {
			continue // reported by TestOpenAPIMatchesRoutingTable
		}

		// An explicit empty list overrides the global requirement: public.
		documentedPublic := op.Security != nil && len(*op.Security) == 0

		if documentedPublic != route.Public {
			t.Errorf("%s: spec says public=%v, server says public=%v",
				key, documentedPublic, route.Public)
		}
	}
}

// Public endpoints must still be reachable without a token, which is the claim
// the spec is actually making.
//
// The check is on the error code, not the status: POSTing an empty body to
// /v1/auth/login is legitimately a 401, but INVALID_CREDENTIALS means the
// request was let through and the credentials were wrong. Only the token codes
// mean "you needed to authenticate first".
func TestPublicOperationsNeedNoToken(t *testing.T) {
	e := newEnv(t)

	for _, route := range e.api.Routes() {
		key := route.Method + " " + route.Pattern
		if !route.Public || strings.Contains(route.Pattern, "{") {
			continue
		}
		// The docs page answers in HTML, not the JSON envelope.
		if slices.Contains(documentationRoutes, key) {
			continue
		}

		res, body := e.do(route.Method, route.Pattern, "", map[string]any{})
		if res.StatusCode != http.StatusUnauthorized {
			continue
		}

		envelope, _ := body["error"].(map[string]any)
		switch str(envelope["code"]) {
		case httpx.CodeTokenInvalid, httpx.CodeTokenExpired:
			t.Errorf("%s is documented public but demanded a token: %v", key, body)
		}
	}
}

// Protected endpoints must actually reject an anonymous caller.
func TestProtectedOperationsRejectAnonymousCallers(t *testing.T) {
	e := newEnv(t)

	for _, route := range e.api.Routes() {
		if route.Public {
			continue
		}
		// Substitute something concrete for path wildcards; the request should
		// be rejected before the value is ever looked up.
		path := strings.NewReplacer("{username}", "ada", "{id}", "some-id").Replace(route.Pattern)

		res, body := e.do(route.Method, path, "", map[string]any{})
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s is protected but an anonymous request returned %d: %v",
				route.Method, path, res.StatusCode, body)
		}
	}
}

func TestOpenAPIDocumentIsWellFormed(t *testing.T) {
	doc := loadSpec(t)

	if !strings.HasPrefix(doc.OpenAPI, "3.") {
		t.Errorf("openapi version: got %q, want 3.x", doc.OpenAPI)
	}
	if doc.Info.Title == "" || doc.Info.Version == "" {
		t.Error("info.title and info.version are both required")
	}
	if len(doc.Servers) == 0 {
		t.Error("no servers listed, so Try It Out has nothing to call")
	}
	if _, ok := doc.Components.SecuritySchemes["bearerAuth"]; !ok {
		t.Error("the bearerAuth security scheme is referenced but not defined")
	}

	seenOperationIDs := make(map[string]string)
	for key, op := range specOperations(t, loadSpec(t)) {
		if op.Summary == "" {
			t.Errorf("%s has no summary", key)
		}
		if len(op.Tags) == 0 {
			t.Errorf("%s has no tag, so it will not be grouped in the UI", key)
		}
		if op.OperationID == "" {
			t.Errorf("%s has no operationId, which client generators need", key)
			continue
		}
		// Duplicate operation IDs silently break generated clients.
		if previous, clash := seenOperationIDs[op.OperationID]; clash {
			t.Errorf("operationId %q is used by both %s and %s", op.OperationID, previous, key)
		}
		seenOperationIDs[op.OperationID] = key

		if len(op.Responses) == 0 {
			t.Errorf("%s documents no responses", key)
		}
	}
}

// Every protected operation should say what a 401 looks like, since handling it
// is the single most important thing a client does.
func TestProtectedOperationsDocumentUnauthorized(t *testing.T) {
	doc := loadSpec(t)

	var missing []string
	for key, op := range specOperations(t, doc) {
		isPublic := op.Security != nil && len(*op.Security) == 0
		if isPublic {
			continue
		}
		if _, ok := op.Responses["401"]; !ok {
			missing = append(missing, key)
		}
	}

	sort.Strings(missing)
	for _, key := range missing {
		t.Errorf("%s is protected but documents no 401 response", key)
	}
}

func TestSpecIsServed(t *testing.T) {
	e := newEnv(t)

	res, err := e.server.Client().Get(e.server.URL + api.OpenAPIPath)
	if err != nil {
		t.Fatalf("fetching the spec: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); !strings.Contains(got, "yaml") {
		t.Errorf("Content-Type: got %q, want a YAML type", got)
	}
}

func TestDocsPageIsServedAndPointsAtTheSpec(t *testing.T) {
	e := newEnv(t)

	res, err := e.server.Client().Get(e.server.URL + api.DocsPath)
	if err != nil {
		t.Fatalf("fetching the docs page: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Errorf("Content-Type: got %q, want text/html", got)
	}

	page := make([]byte, 4096)
	n, _ := res.Body.Read(page)
	// A docs page that points at the wrong spec URL renders an empty UI, which
	// is a confusing failure to debug by hand.
	if !strings.Contains(string(page[:n]), api.OpenAPIPath) {
		t.Errorf("the docs page does not reference %s", api.OpenAPIPath)
	}
}

func TestDocsCanBeDisabled(t *testing.T) {
	e := newEnvWithDocs(t, false)

	for _, path := range []string{api.DocsPath, api.OpenAPIPath} {
		res, _ := e.do(http.MethodGet, path, "", nil)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s with DocsEnabled=false: got %d, want 404", path, res.StatusCode)
		}
	}
}
