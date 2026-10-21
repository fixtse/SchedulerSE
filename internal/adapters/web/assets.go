package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// Public assets use content-derived versions; authenticated HTML remains no-store.
type staticAsset struct {
	path, contentType, body, version string
}

func newStaticAsset(path, contentType, body string) staticAsset {
	sum := sha256.Sum256([]byte(body))
	return staticAsset{path: path, contentType: contentType, body: body, version: hex.EncodeToString(sum[:])}
}

var styleAsset = newStaticAsset("/style.css", "text/css; charset=utf-8", stylesheet)
var htmxAsset = newStaticAsset("/htmx.js", "text/javascript; charset=utf-8", htmxJS)
var scriptAsset = newStaticAsset("/schedule.js", "text/javascript; charset=utf-8", scheduleJS)

var logoVectorAsset = newStaticAsset("/logo-full.svg", "image/svg+xml", string(logoFullSVG))
var logoFullAsset = newStaticAsset("/logo-full.png", "image/png", string(logoFullPNG))
var logoMiniAsset = newStaticAsset("/logo-mini.png", "image/png", string(logoMiniPNG))

var uiAssetVersion = digest(styleAsset.version + scriptAsset.version + htmxAsset.version + logoFullAsset.version + logoMiniAsset.version + logoVectorAsset.version)

func (a staticAsset) URL() string { return a.path + "?v=" + a.version }

func (a staticAsset) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", a.contentType)
	w.Header().Set("ETag", `"`+a.version+`"`)
	if r.URL.Query().Get("v") == a.version {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// Old pages and direct/unversioned requests must revalidate across releases.
		w.Header().Set("Cache-Control", "public, no-cache")
	}
	http.ServeContent(w, r, a.path, time.Time{}, strings.NewReader(a.body))
}
