package web

import (
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestEnhancedNavigationPreservesNormalFormsAndPrivateHistory(t *testing.T) {
	f := fixture(t)
	req := httptest.NewRequest("GET", "/setup", nil)
	res := httptest.NewRecorder()
	f.app.ServeHTTP(res, req)
	body := res.Body.String()
	if res.Code != 200 || !strings.Contains(body, `hx-boost="true"`) || !strings.Contains(body, `hx-history="false"`) {
		t.Fatal("enhanced navigation missing")
	}
	if !strings.Contains(res.Header().Get("Content-Security-Policy"), "connect-src 'self'") || strings.Contains(res.Header().Get("Content-Security-Policy"), "unsafe-") {
		t.Fatal("navigation CSP unavailable or relaxed")
	}
	if !strings.Contains(body, `src="`+htmxAsset.URL()+`"`) {
		t.Fatal("navigation library not loaded locally with a versioned URL")
	}
	meta := regexp.MustCompile(`name="htmx-config" content='([^']+)'`).FindStringSubmatch(body)
	var config struct {
		HistoryCacheSize       int  `json:"historyCacheSize"`
		AllowEval              bool `json:"allowEval"`
		AllowScriptTags        bool `json:"allowScriptTags"`
		IncludeIndicatorStyles bool `json:"includeIndicatorStyles"`
		SelfRequestsOnly       bool `json:"selfRequestsOnly"`
	}
	if len(meta) != 2 || json.Unmarshal([]byte(meta[1]), &config) != nil {
		t.Fatal("invalid navigation configuration")
	}
	if config.HistoryCacheSize != 0 || config.AllowEval || config.AllowScriptTags || config.IncludeIndicatorStyles || !config.SelfRequestsOnly {
		t.Fatal("navigation may persist private history or require inline code/styles")
	}
	// Every form in the shared template must retain native submission and redirect semantics.
	for _, form := range regexp.MustCompile(`<form\b[^>]*>`).FindAllString(pageHTML, -1) {
		if !strings.Contains(form, `hx-boost="false"`) {
			t.Fatal("form accidentally opted into navigation swaps", form)
		}
	}
	for _, path := range []string{"/settings/agent/delivery.zip", "/settings/agent/guide.md"} {
		pattern := regexp.MustCompile(`<a\b[^>]*href="` + regexp.QuoteMeta(path) + `"[^>]*>`)
		link := pattern.FindString(pageHTML)
		if link == "" || !strings.Contains(link, `hx-boost="false"`) {
			t.Fatal("file download or raw guide would be intercepted", path)
		}
	}
	// Boosted requests receive the same complete document, preserving title and no-JS fallback.
	req = httptest.NewRequest("GET", "/setup", nil)
	req.Header.Set("HX-Boosted", "true")
	res = httptest.NewRecorder()
	f.app.ServeHTTP(res, req)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "<!doctype html>") || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("enhanced navigation broke full documents or cached account pages")
	}
}

func TestOpenTabReloadsAfterAssetsChange(t *testing.T) {
	f := fixture(t)
	for _, tc := range []struct {
		version string
		want    int
	}{
		{"previous-release", 204}, {uiAssetVersion, 200},
	} {
		req := httptest.NewRequest("GET", "/setup", nil)
		req.Header.Set("HX-Boosted", "true")
		req.Header.Set("X-SchedulerSE-UI", tc.version)
		res := httptest.NewRecorder()
		f.app.ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Fatal("incorrect release navigation status", res.Code)
		}
		if tc.want == 204 {
			if res.Header().Get("HX-Refresh") != "true" || res.Body.Len() != 0 {
				t.Fatal("stale page did not request a document reload")
			}
		} else if res.Header().Get("HX-Refresh") != "" || !strings.Contains(res.Body.String(), uiAssetVersion) {
			t.Fatal("current tab reloaded or omitted its asset version")
		}
	}
}
