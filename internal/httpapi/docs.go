package httpapi

// Swagger UI at /docs, over the generated document.
//
// The assets are embedded, not fetched from a CDN: an engine that runs behind
// its own egress firewall — or on a machine with no internet at all — must
// still be able to show its own API. Two files, vendored under swaggerui/ and
// refreshed by `task api:swagger-ui`.

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed swaggerui/swagger-ui-bundle.js swaggerui/swagger-ui.css swaggerui/LICENSE
var swaggerAssets embed.FS

// docsPage is the whole UI: the bundle, the stylesheet, and the one call that
// points it at our document.
const docsPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Apothecary API</title>
  <link rel="stylesheet" href="/docs/swagger-ui.css">
  <style>body { margin: 0 } .topbar { display: none }</style>
</head>
<body>
  <div id="ui"></div>
  <script src="/docs/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: "/api/openapi.json",
      dom_id: "#ui",
      deepLinking: true,
      docExpansion: "none",
      defaultModelsExpandDepth: 0,
      tryItOutEnabled: true
    });
  </script>
</body>
</html>
`

// serveDocs serves the page at /docs and its two assets under /docs/.
func (s *Server) serveDocs(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/docs")
	name = strings.TrimPrefix(name, "/")
	switch name {
	case "":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(docsPage))
	case "swagger-ui-bundle.js", "swagger-ui.css", "LICENSE":
		data, err := swaggerAssets.ReadFile("swaggerui/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasSuffix(name, ".js"):
			w.Header().Set("Content-Type", "application/javascript")
		case strings.HasSuffix(name, ".css"):
			w.Header().Set("Content-Type", "text/css")
		default:
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(data)
	default:
		http.NotFound(w, r)
	}
}
