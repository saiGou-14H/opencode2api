package admin

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"opencode2api/webui"
)

func assetFixture() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte(`<link href="/css/base.css"><script type="module" src="/js/main.js"></script>`)},
		"css/base.css":       {Data: []byte("body {}")},
		"js/main.js":         {Data: []byte(`import './pages/config.js';`)},
		"js/pages/config.js": {Data: []byte(`import '../store.js'; export const sources = 10;`)},
		"js/store.js":        {Data: []byte(`export const state = {};`)},
	}
}

func assetTestHandler(t *testing.T, assets fs.FS) http.Handler {
	t.Helper()
	handler, err := newAssetHandler(assets)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func assetResponse(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(method, target, nil))
	return w
}

func assetPrefix(t *testing.T, handler http.Handler) string {
	t.Helper()
	body := assetResponse(handler, "GET", "/").Body.String()
	prefix := regexp.MustCompile(`/ui-assets/[0-9a-f]{64}`).FindString(body)
	if prefix == "" {
		t.Fatalf("missing asset prefix: %s", body)
	}
	return prefix
}

func TestAssetNamespaceDependencies(t *testing.T) {
	assets := assetFixture()
	handler := assetTestHandler(t, assets)
	prefix := assetPrefix(t, handler)
	for _, name := range []string{"js/main.js", "js/pages/config.js", "js/store.js", "css/base.css"} {
		r := httptest.NewRequest("GET", prefix+"/"+name, nil)
		originalURL := r.URL.String()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 || w.Body.String() != string(assets[name].Data) {
			t.Fatalf("%s: status=%d body=%q", name, w.Code, w.Body.String())
		}
		if r.URL.String() != originalURL {
			t.Fatal("handler mutated request URL")
		}
		if w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("%s cache: %v", name, w.Header())
		}
	}
	base, _ := url.Parse(prefix + "/js/pages/config.js")
	dependency, _ := url.Parse("../store.js")
	if got := assetResponse(handler, "GET", base.ResolveReference(dependency).String()); got.Code != 200 {
		t.Fatalf("relative module dependency: %d", got.Code)
	}
}

func TestAssetHashTracksRuntimeBytes(t *testing.T) {
	original := assetPrefix(t, assetTestHandler(t, assetFixture()))
	for _, name := range []string{"index.html", "css/base.css", "js/main.js", "js/pages/config.js", "js/store.js"} {
		t.Run(name, func(t *testing.T) {
			assets := assetFixture()
			assets[name].Data = append(assets[name].Data, '\n')
			if assetPrefix(t, assetTestHandler(t, assets)) == original {
				t.Fatal("runtime edit did not change hash")
			}
		})
	}
	assets := assetFixture()
	assets["tests/test.js"] = &fstest.MapFile{Data: []byte("ignored")}
	assets["js/__tests__/test.js"] = &fstest.MapFile{Data: []byte("ignored")}
	assets["js/config.test.js"] = &fstest.MapFile{Data: []byte("ignored")}
	assets["js/README.md"] = &fstest.MapFile{Data: []byte("ignored")}
	if assetPrefix(t, assetTestHandler(t, assets)) != original {
		t.Fatal("hash is nondeterministic or includes non-runtime files")
	}
	assets["js/new.js"] = &fstest.MapFile{Data: []byte("new")}
	if assetPrefix(t, assetTestHandler(t, assets)) == original {
		t.Fatal("new module did not change hash")
	}
}

func TestAssetIndexAndLegacyNoStore(t *testing.T) {
	handler := assetTestHandler(t, assetFixture())
	prefix := assetPrefix(t, handler)
	for _, target := range []string{"/", "/index.html", "/settings/route", "/js/main.js", "/css/base.css"} {
		for _, method := range []string{"GET", "HEAD"} {
			w := assetResponse(handler, method, target)
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s %s: status=%d headers=%v", method, target, w.Code, w.Header())
			}
			if method == "HEAD" && w.Body.Len() != 0 {
				t.Fatalf("HEAD %s returned body", target)
			}
			if method == "GET" && !strings.HasPrefix(target, "/js/") && !strings.HasPrefix(target, "/css/") {
				if !strings.Contains(w.Body.String(), `src="`+prefix+`/js/main.js"`) || !strings.Contains(w.Body.String(), `href="`+prefix+`/css/base.css"`) {
					t.Fatalf("index resources were not rewritten: %s", w.Body.String())
				}
			}
		}
	}
}

func TestAssetInvalidNamespaceNotSPA(t *testing.T) {
	handler := assetTestHandler(t, assetFixture())
	prefix := assetPrefix(t, handler)
	for _, target := range []string{
		"/ui-assets", "/ui-assets/", "/ui-assets/stale/js/main.js", prefix + "extra/js/main.js",
		prefix + "/js/missing.js", prefix + "/index.html", prefix + "/js/", prefix + "/js/../js/main.js",
		prefix + "/css/missing.css", "/js/missing.js", "/css/missing.css", "/api/missing",
	} {
		w := assetResponse(handler, "GET", target)
		if w.Code != 404 || strings.Contains(w.Body.String(), "<script") || strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("%s: status=%d body=%q headers=%v", target, w.Code, w.Body.String(), w.Header())
		}
	}
}

func TestEmbeddedAssetNamespace(t *testing.T) {
	handler := (&Server{}).securityHeaders((&Server{}).staticHandler())
	prefix := assetPrefix(t, handler)
	names, err := runtimeAssetNames(webui.Assets)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "index.html" {
			continue
		}
		w := assetResponse(handler, "GET", prefix+"/"+name)
		want, err := fs.ReadFile(webui.Assets, name)
		if err != nil || w.Code != 200 || w.Body.String() != string(want) {
			t.Fatalf("embedded %s: err=%v status=%d", name, err, w.Code)
		}
		if w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("security headers not preserved")
		}
	}
}
