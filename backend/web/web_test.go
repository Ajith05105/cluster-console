package web

// Automated tests for serving the page.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// builtPage is a made-up build output, shaped like what Vite produces.
var builtPage = fstest.MapFS{
	"index.html":             {Data: []byte("<html>the page</html>")},
	"assets/index-abc123.js": {Data: []byte("console.log('the script')")},
}

// get asks the handler for a path and returns the reply.
func get(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, target, nil))
	return response
}

func TestServesThePageAndItsAssets(t *testing.T) {
	handler := handlerFor(builtPage)

	// The page, fetched fresh every time.
	page := get(t, handler, http.MethodGet, "/")
	if page.Code != http.StatusOK || page.Body.String() != "<html>the page</html>" {
		t.Errorf("GET /: status %d, body %q", page.Code, page.Body.String())
	}
	if got := page.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("GET /: Cache-Control = %q, want no-cache", got)
	}
	if got := page.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("GET /: Content-Type = %q, want text/html", got)
	}

	// An asset, which browsers may keep.
	script := get(t, handler, http.MethodGet, "/assets/index-abc123.js")
	if script.Code != http.StatusOK || script.Body.String() != "console.log('the script')" {
		t.Errorf("GET asset: status %d, body %q", script.Code, script.Body.String())
	}
	if got := script.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("GET asset: Cache-Control = %q, want it to allow keeping the file", got)
	}
	if got := script.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Errorf("GET asset: Content-Type = %q, want javascript", got)
	}
}

func TestUnknownAddressesGetThePage(t *testing.T) {
	handler := handlerFor(builtPage)

	for _, target := range []string{"/index.html", "/anything", "/some/deep/link", "/assets"} {
		reply := get(t, handler, http.MethodGet, target)
		if reply.Code != http.StatusOK || reply.Body.String() != "<html>the page</html>" {
			t.Errorf("GET %s: status %d, body %q; want the page", target, reply.Code, reply.Body.String())
		}
	}
	// A missing asset is a real "not found", not the page.
	if reply := get(t, handler, http.MethodGet, "/assets/old-script.js"); reply.Code != http.StatusNotFound {
		t.Errorf("GET of a missing asset: status %d, want 404", reply.Code)
	}
	// Only reading is allowed here.
	if reply := get(t, handler, http.MethodPost, "/"); reply.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /: status %d, want 405", reply.Code)
	}
	// Attempts to climb out of the folder get the page, never another file.
	for _, target := range []string{"/../web.go", "/assets/../../web.go", "/%2e%2e/web.go"} {
		reply := get(t, handler, http.MethodGet, target)
		if strings.Contains(reply.Body.String(), "package web") {
			t.Errorf("GET %s leaked a file from outside the page folder", target)
		}
	}
}

// The placeholder committed in this repository is served by the real Handler.
func TestEmbeddedPlaceholderIsServed(t *testing.T) {
	reply := get(t, Handler(), http.MethodGet, "/")
	if reply.Code != http.StatusOK || !strings.Contains(reply.Body.String(), "Cluster console") {
		t.Errorf("GET /: status %d, body %.60q", reply.Code, reply.Body.String())
	}
}
