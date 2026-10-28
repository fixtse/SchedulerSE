package web

import (
	"github.com/fixtse/SchedulerSE/internal/domain"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHistoryPagesAndExpandableDetails(t *testing.T) {
	f := fixture(t)
	if err := f.store.CreateAccount("owner", []byte("unused-test-hash")); err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddSession(digest("test-session"), "test-csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	serverURL, _ := url.Parse(f.server.URL)
	f.client.Jar.SetCookies(serverURL, []*http.Cookie{{Name: "session", Value: "test-session", Path: "/"}})
	_, err := f.store.DB.Exec(`WITH RECURSIVE sequence(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM sequence WHERE n<52)
 INSERT INTO runs(job,slot,status,notification,started,finished,job_name,job_revision,timeout_ms,http_status,error_code,duration_ms)
 SELECT 'report',printf('manual:run-%02d',n),'failed','none','2026-10-20 12:00:00','2026-10-20 12:00:02','<script>alert(1)</script>',3,60000,503,'HTTP_ERROR',1234 FROM sequence`)
	if err != nil {
		t.Fatal(err)
	}
	code, body := f.get(t, "/")
	// Browsers collapse template indentation and line breaks between words.
	body = strings.Join(strings.Fields(body), " ")
	for _, fragment := range []string{"Showing 1–25 of 52", "Page 1 of 3", `href="/?page=2#activity"`, `<details class="activity-run">`, "The endpoint returned an unsuccessful HTTP status.", "1.234s", "60000 ms", "HTTP_ERROR", "manual:run-52"} {
		if code != 200 || !strings.Contains(body, fragment) {
			t.Fatalf("missing history detail %q: %d", fragment, code)
		}
	}
	if strings.Contains(body, "manual:run-01") || strings.Contains(body, "private-header") || strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("history leaked a secret, included another page, or rendered unsafe task names")
	}
	code, body = f.get(t, "/?page=2")
	body = strings.Join(strings.Fields(body), " ")
	if code != 200 || !strings.Contains(body, "Showing 26–50 of 52") || !strings.Contains(body, "manual:run-03") || strings.Contains(body, "manual:run-52") || !strings.Contains(body, `rel="prev"`) {
		t.Fatal("history did not navigate to the next page")
	}
	code, body = f.get(t, "/?page=3")
	body = strings.Join(strings.Fields(body), " ")
	if code != 200 || !strings.Contains(body, "Showing 51–52 of 52") || !strings.Contains(body, "manual:run-01") || strings.Contains(body, `rel="next"`) {
		t.Fatal("last history page is incorrect")
	}
	response, err := f.client.Get(f.server.URL + "/?page=999")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 303 || response.Header.Get("Location") != "/?page=3#activity" {
		t.Fatal("out-of-range history did not redirect to the last page")
	}
	for _, value := range []string{"0", "-1", "invalid", "999999999999999999999999999"} {
		if code, _ := f.get(t, "/?page="+value); code != 400 {
			t.Fatalf("invalid history page %q accepted", value)
		}
	}
	code, body = f.get(t, "/?status=failed")
	if code != 200 || !strings.Contains(body, `value="failed" selected`) || !strings.Contains(body, `page=2&amp;status=failed#activity`) {
		t.Fatal("filter selection or pagination was lost")
	}
	response, err = f.client.Get(f.server.URL + "/?page=999&status=failed")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 303 || response.Header.Get("Location") != "/?page=3&status=failed#activity" {
		t.Fatal("clamped page lost status filter", response.Header)
	}
	code, body = f.get(t, "/?status=success")
	if code != 200 || !strings.Contains(body, "No matching executions") || strings.Contains(body, `<details class="activity-run">`) {
		t.Fatal("empty status filter did not show its own empty state")
	}
	if code, _ = f.get(t, "/?status=invalid"); code != 400 {
		t.Fatal("invalid status accepted", code)
	}
}

func TestLegacyAndRunningExecutionDetails(t *testing.T) {
	legacy := executionView(domain.Run{Job: "old-job", Slot: "manual:old", Status: "success", Started: "2026-10-20 12:00:00", Finished: "2026-10-20 12:00:02", DurationMS: -1})
	if legacy.JobName != "old-job" || legacy.Trigger != "Manual" || legacy.Duration != "2s (approx.)" || legacy.HTTPStatus != 0 {
		t.Fatal("legacy execution details changed or were invented")
	}
	interrupted := executionView(domain.Run{Status: "interrupted", DurationMS: -1})
	if !strings.Contains(interrupted.Description, "will not resume automatically") {
		t.Fatal("interrupted run omitted its uncertain result")
	}
}
