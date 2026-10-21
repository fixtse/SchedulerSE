package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersionedAssetsCacheWithoutCachingAccountPages(t *testing.T) {
	f := fixture(t)
	for _, asset := range []staticAsset{styleAsset, scriptAsset, htmxAsset, logoFullAsset, logoMiniAsset, logoVectorAsset} {
		req := httptest.NewRequest("GET", asset.URL(), nil)
		res := httptest.NewRecorder()
		f.app.ServeHTTP(res, req)
		if res.Code != 200 || res.Body.String() != asset.body || res.Header().Get("Content-Type") != asset.contentType || res.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("versioned asset %s was not served cacheably: %d %+v", asset.path, res.Code, res.Header())
		}
		etag := res.Header().Get("ETag")
		if etag == "" {
			t.Fatal("asset has no validator")
		}
		req = httptest.NewRequest("GET", asset.URL(), nil)
		req.Header.Set("If-None-Match", etag)
		res = httptest.NewRecorder()
		f.app.ServeHTTP(res, req)
		if res.Code != 304 || res.Body.Len() != 0 {
			t.Fatal("asset revalidation did not return an empty 304", res.Code)
		}
		for _, path := range []string{asset.path, asset.path + "?v=old-release"} {
			req = httptest.NewRequest("GET", path, nil)
			res = httptest.NewRecorder()
			f.app.ServeHTTP(res, req)
			if res.Code != 200 || res.Header().Get("Cache-Control") != "public, no-cache" {
				t.Fatal("legacy URL could retain stale assets", res.Code, res.Header())
			}
		}
		req = httptest.NewRequest("HEAD", asset.URL(), nil)
		res = httptest.NewRecorder()
		f.app.ServeHTTP(res, req)
		if res.Code != 200 || res.Body.Len() != 0 || res.Header().Get("ETag") != etag {
			t.Fatal("asset HEAD response incorrect", res.Code)
		}
		req = httptest.NewRequest("POST", asset.URL(), nil)
		res = httptest.NewRecorder()
		f.app.ServeHTTP(res, req)
		if res.Code != 405 || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("asset mutation accepted or cacheable", res.Code)
		}
	}
	req := httptest.NewRequest("GET", "/setup", nil)
	res := httptest.NewRecorder()
	f.app.ServeHTTP(res, req)
	if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("account page became cacheable", res.Code)
	}
	body := res.Body.String()
	if !strings.Contains(body, `href="`+styleAsset.URL()+`"`) || !strings.Contains(body, `src="`+scriptAsset.URL()+`"`) {
		t.Fatal("HTML did not reference versioned assets")
	}
	headEnd := strings.Index(body, "</head>")
	if headEnd < 0 || !strings.Contains(body[:headEnd], `rel="stylesheet"`) {
		t.Fatal("stylesheet no longer blocks rendering from the head")
	}
}

func TestAssetVersionChangesWithContent(t *testing.T) {
	before := newStaticAsset("/style.css", "text/css", "body{color:black}")
	after := newStaticAsset("/style.css", "text/css", "body{color:green}")
	if before.URL() == after.URL() {
		t.Fatal("CSS changes did not invalidate cached URLs")
	}
	unchanged := newStaticAsset("/style.css", "text/css", before.body)
	if before.URL() != unchanged.URL() {
		t.Fatal("identical assets received different cache keys")
	}
}
