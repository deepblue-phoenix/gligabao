package admin_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/deepblue-phoenix/gligabao/internal/api/admin"
)

func TestResolveExtensions(t *testing.T) {
	ok := fstest.MapFS{"index.js": {Data: []byte("export default () => {}")}}
	got, err := admin.ResolveExtensions([]admin.Extension{{Name: "b", Assets: ok}, {Name: "a-1_x", Assets: ok}})
	if err != nil || len(got) != 2 || got[0].Name != "a-1_x" {
		t.Fatalf("got %v, %v", got, err)
	}
	for name, list := range map[string][]admin.Extension{
		"empty name":   {{Assets: ok}},
		"upper case":   {{Name: "Abc", Assets: ok}},
		"bad char":     {{Name: "a.b", Assets: ok}},
		"digit first":  {{Name: "1a", Assets: ok}},
		"duplicate":    {{Name: "a", Assets: ok}, {Name: "a", Assets: ok}},
		"no assets":    {{Name: "a"}},
		"no index.js":  {{Name: "a", Assets: fstest.MapFS{"x.js": {}}}},
		"index is dir": {{Name: "a", Assets: fstest.MapFS{"index.js/x": {}}}},
	} {
		if _, err := admin.ResolveExtensions(list); !errors.Is(err, admin.ErrInvalidExtension) {
			t.Errorf("%s: err = %v, want ErrInvalidExtension", name, err)
		}
	}
}

// TestExtensions checks the list, the assets and the API of an extension, next to the admin UI's catch-all route
// (registering them on one mux panics if the patterns conflict).
func TestExtensions(t *testing.T) {
	api := http.NewServeMux()
	api.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write(b)
	})
	exts, err := admin.ResolveExtensions([]admin.Extension{
		{Name: "full", Handler: api, Assets: fstest.MapFS{
			"index.js":  {Data: []byte("// full")},
			"style.css": {Data: []byte("/* css */")},
			"lib/x.js":  {Data: []byte("// x")},
		}},
		{Name: "plain", Assets: fstest.MapFS{"index.js": {Data: []byte("// plain")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/gb/admin/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("core")) })
	admin.RegisterExtensions(mux, exts)

	do := func(method, url, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, url, strings.NewReader(body)))
		return rec
	}

	rec := do("GET", "/v1/gb/admin/api/extensions", "")
	var list struct {
		APIVersion int `json:"api_version"`
		Extensions []struct{ Name, Script, Style string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != 200 {
		t.Fatalf("list: %d %s %v", rec.Code, rec.Body, err)
	}
	if list.APIVersion != admin.ExtensionAPIVersion || len(list.Extensions) != 2 ||
		list.Extensions[0].Name != "full" || list.Extensions[0].Script != "ext/full/index.js" || list.Extensions[0].Style != "ext/full/style.css" ||
		list.Extensions[1].Name != "plain" || list.Extensions[1].Style != "" {
		t.Fatalf("list = %+v", list)
	}

	for url, want := range map[string]string{
		"/v1/gb/admin/ext/full/index.js":  "// full",
		"/v1/gb/admin/ext/full/style.css": "/* css */",
		"/v1/gb/admin/ext/full/lib/x.js":  "// x",
		"/v1/gb/admin/ext/plain/index.js": "// plain",
		"/v1/gb/admin/":                   "core",
	} {
		if rec := do("GET", url, ""); rec.Code != 200 || rec.Body.String() != want {
			t.Errorf("GET %s: %d %q, want %q", url, rec.Code, rec.Body, want)
		}
	}
	if rec := do("GET", "/v1/gb/admin/ext/full/index.js", ""); rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if rec := do("POST", "/v1/gb/admin/ext/full/api/echo", "hi"); rec.Code != 200 || rec.Body.String() != "hi" {
		t.Errorf("api: %d %q", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		method, url string
		want        int
	}{
		{"GET", "/v1/gb/admin/ext/full/missing.js", 404},
		{"GET", "/v1/gb/admin/ext/full/api/nope", 404},   // the extension's own mux answers
		{"GET", "/v1/gb/admin/ext/plain/api/echo", 404},  // no Handler
		{"POST", "/v1/gb/admin/ext/plain/index.js", 405}, // assets are GET only
		{"GET", "/v1/gb/admin/ext/other/index.js", 200},  // not an extension: the admin UI's own handler
	} {
		if rec := do(c.method, c.url, ""); rec.Code != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.url, rec.Code, c.want)
		}
	}
}
