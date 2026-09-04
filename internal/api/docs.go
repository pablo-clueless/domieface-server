package api

import (
	_ "embed"
	"net/http"
)

// OpenAPISpec is the API contract as OpenAPI 3.1, embedded so a built binary
// serves its own documentation with no files to ship alongside it.
//
// It is hand-written rather than generated from annotations: the contract came
// first, and annotations scattered through handlers drift from it quietly.
// TestOpenAPIMatchesRoutingTable is what stops this file drifting instead.
//
//go:embed openapi.yaml
var OpenAPISpec []byte

// Paths the documentation is served from. Both sit outside /v1: they describe
// the API rather than being part of it.
const (
	// DocsPath serves the Swagger UI page.
	DocsPath = "/docs"
	// OpenAPIPath serves the raw spec.
	OpenAPIPath = "/openapi.yaml"
)

// swaggerUIVersion is pinned. An unpinned CDN URL means the docs page can break
// on someone else's release schedule.
const swaggerUIVersion = "5.17.14"

// docsPage renders the spec with Swagger UI. The UI assets come from a CDN
// rather than being vendored: swagger-ui-dist is several megabytes, and this is
// a documentation page, not a runtime dependency of the API. The trade-off is
// that the page needs internet access to render — the spec itself, at
// /openapi.yaml, is served from the binary and always works offline.
var docsPage = []byte(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Social API — reference</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui.css">
  <style>
    body { margin: 0; background: #fafafa; }
    .swagger-ui .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({
        url: "/openapi.yaml",
        dom_id: "#swagger-ui",
        deepLinking: true,
        displayRequestDuration: true,
        // Sort by the order in the spec rather than alphabetically, so the
        // reading order is register -> login -> refresh rather than scrambled.
        operationsSorter: null,
        tagsSorter: null,
        persistAuthorization: true,
        tryItOutEnabled: true
      });
    };
  </script>
</body>
</html>
`)

// handleOpenAPISpec serves the raw spec. Kept separate from the UI so tooling
// (client generators, contract tests, Postman) can fetch it directly.
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// no-cache, not a max-age: the spec must always match the binary serving
	// it. A cached copy after a deploy documents the previous version, and
	// 30 KB is not worth that confusion.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(OpenAPISpec)
	return nil
}

// handleDocs serves the Swagger UI page.
func (s *Server) handleDocs(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(docsPage)
	return nil
}
