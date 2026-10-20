package admin

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Only local resource attributes are rewritten; module source stays unchanged so
// relative imports resolve inside the same content-addressed namespace.
var assetURLAttribute = regexp.MustCompile(`(?i)(\b(?:src|href)\s*=\s*["'])(/(?:js|css)/)`)

// runtimeAssetNames deliberately visits only the two runtime trees, not the
// entire embedded filesystem (which may also contain test fixtures or sources).
func runtimeAssetNames(assets fs.FS) ([]string, error) {
	names := []string{"index.html"}
	var visit func(string) error
	visit = func(dir string) error {
		entries, err := fs.ReadDir(assets, dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || name == "tests" || name == "__tests__" || name == "test" {
				continue
			}
			filename := path.Join(dir, name)
			if entry.IsDir() {
				if err := visit(filename); err != nil {
					return err
				}
				continue
			}
			if (strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".css")) && !strings.HasSuffix(name, ".test.js") && !strings.HasSuffix(name, ".spec.js") {
				names = append(names, filename)
			}
		}
		return nil
	}
	for _, dir := range []string{"js", "css"} {
		if err := visit(dir); err != nil {
			return nil, err
		}
	}
	sort.Strings(names)
	return names, nil
}

func newAssetHandler(assets fs.FS) (http.Handler, error) {
	names, err := runtimeAssetNames(assets)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	allowed := make(map[string]bool, len(names))
	var index []byte
	for _, name := range names {
		content, err := fs.ReadFile(assets, name)
		if err != nil {
			return nil, err
		}
		// Length framing makes both paths and bytes part of an unambiguous hash.
		fmt.Fprintf(hash, "%d:%s%d:", len(name), name, len(content))
		hash.Write(content)
		if name == "index.html" {
			index = content
		} else {
			allowed[name] = true
		}
	}
	prefix := fmt.Sprintf("/ui-assets/%x", hash.Sum(nil))
	index = assetURLAttribute.ReplaceAll(index, []byte("${1}"+prefix+"${2}"))
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		urlPath := r.URL.Path
		if strings.HasPrefix(urlPath, "/api/") {
			writeAdminError(w, http.StatusNotFound, "not_found", "management endpoint not found")
			return
		}
		if urlPath == "/ui-assets" || strings.HasPrefix(urlPath, "/ui-assets/") {
			name, ok := strings.CutPrefix(urlPath, prefix+"/")
			if !ok || !allowed[name] {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			mapped := r.Clone(r.Context())
			mapped.URL.Path = "/" + name
			mapped.URL.RawPath = ""
			files.ServeHTTP(w, mapped)
			return
		}
		if urlPath != "/" && urlPath != "/index.html" {
			name := strings.TrimPrefix(urlPath, "/")
			if info, err := fs.Stat(assets, name); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
			if urlPath == "/js" || urlPath == "/css" || strings.HasPrefix(urlPath, "/js/") || strings.HasPrefix(urlPath, "/css/") {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	}), nil
}
