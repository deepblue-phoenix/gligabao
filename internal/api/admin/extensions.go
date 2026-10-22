package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// ExtensionAPIVersion is the version of the JavaScript API (the `gb` object, see admin_ui/src/extensions.ts) that extensions
// are loaded with. It changes only when the API changes in a way that can break an extension.
const ExtensionAPIVersion = 1

// MaxExtensions and MaxExtensionNameLen bound what can be registered.
const (
	MaxExtensions       = 50
	MaxExtensionNameLen = 40
)

// ErrInvalidExtension is wrapped by the error ResolveExtensions returns for a malformed extension.
var ErrInvalidExtension = errors.New("invalid admin extension")

var extensionNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Extension adds views and UI to the admin UI (see docs/admin-extensions.md).
type Extension struct {
	// Name identifies the extension: lowercase letters, digits, '_' and '-', starting with a letter, unique. It is part of the
	// URLs the extension is served at (/v1/gb/admin/ext/<name>/).
	Name string
	// Assets holds the extension's browser files, rooted at the extension (not at a parent directory). It must contain
	// index.js, an ES module whose default export is a function that is called with the `gb` API object; it may contain
	// style.css, loaded with it, and anything else index.js imports (served as is at /v1/gb/admin/ext/<name>/<path>).
	Assets fs.FS
	// Handler, if set, is the extension's own HTTP API, served at /v1/gb/admin/ext/<name>/api/ with that prefix stripped
	// (the extension's JavaScript calls it with gb.api). It may be called concurrently.
	Handler http.Handler
}

// ResolveExtensions validates the extensions registered at startup (ErrInvalidExtension, with what is wrong, if one is
// malformed) and returns them sorted by name.
func ResolveExtensions(list []Extension) ([]Extension, error) {
	if len(list) > MaxExtensions {
		return nil, fmt.Errorf("%w: at most %d extensions may be registered, got %d", ErrInvalidExtension, MaxExtensions, len(list))
	}
	seen := map[string]bool{}
	out := make([]Extension, 0, len(list))
	for i, e := range list {
		switch {
		case e.Name == "" || len(e.Name) > MaxExtensionNameLen || !extensionNameRE.MatchString(e.Name):
			return nil, fmt.Errorf("%w: extension %d: name %q must be 1-%d lowercase letters, digits, '_' or '-', starting with a letter", ErrInvalidExtension, i, e.Name, MaxExtensionNameLen)
		case seen[e.Name]:
			return nil, fmt.Errorf("%w: duplicate extension name %q", ErrInvalidExtension, e.Name)
		case e.Assets == nil:
			return nil, fmt.Errorf("%w: extension %q has no Assets", ErrInvalidExtension, e.Name)
		}
		if st, err := fs.Stat(e.Assets, "index.js"); err != nil || st.IsDir() {
			return nil, fmt.Errorf("%w: extension %q: Assets has no index.js (it must be rooted at the extension's own directory)", ErrInvalidExtension, e.Name)
		}
		seen[e.Name] = true
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// extensionInfo is what the admin page reads of an extension: where its files are, relative to the admin UI's base URL.
type extensionInfo struct {
	Name   string `json:"name"`
	Script string `json:"script"`
	Style  string `json:"style,omitempty"` // empty: no style.css
}

// RegisterExtensions mounts the (validated) extensions on mux: GET /v1/gb/admin/api/extensions lists them and everything
// under /v1/gb/admin/ext/<name>/ is served by the extension: its Handler under api/ (any method), its Assets otherwise (GET).
func RegisterExtensions(mux *http.ServeMux, exts []Extension) {
	infos := make([]extensionInfo, 0, len(exts))
	for _, e := range exts {
		info := extensionInfo{Name: e.Name, Script: "ext/" + e.Name + "/index.js"}
		if st, err := fs.Stat(e.Assets, "style.css"); err == nil && !st.IsDir() {
			info.Style = "ext/" + e.Name + "/style.css"
		}
		infos = append(infos, info)
	}
	mux.HandleFunc("GET /v1/gb/admin/api/extensions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			APIVersion int             `json:"api_version"`
			Extensions []extensionInfo `json:"extensions"`
		}{ExtensionAPIVersion, infos})
	})
	for _, e := range exts {
		prefix := "/v1/gb/admin/ext/" + e.Name + "/"
		h := extensionHandler(prefix, e)
		// One pattern per method rather than a method-less one: a method-less "/v1/gb/admin/ext/x/" would conflict with the
		// admin UI's "GET /v1/gb/admin/" (each is more specific than the other in one respect), which panics.
		for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			mux.Handle(m+" "+prefix, h)
		}
	}
}

func extensionHandler(prefix string, e Extension) http.Handler {
	files := http.StripPrefix(prefix, http.FileServerFS(e.Assets))
	var api http.Handler
	if e.Handler != nil {
		api = http.StripPrefix(strings.TrimSuffix(prefix, "/")+"/api", e.Handler)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		if strings.HasPrefix(rest, "api/") {
			if api == nil {
				http.NotFound(w, r)
				return
			}
			api.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-cache") // an updated extension is picked up by a reload
		files.ServeHTTP(w, r)
	})
}
