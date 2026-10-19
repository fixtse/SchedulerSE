package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPContractsAndSingleAttempt(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		if r.Method != "POST" {
			t.Error("not POST")
		}
		switch r.URL.Path {
		case "/sync":
			if r.Header.Get("Authorization") != "Bearer test-only" {
				t.Error("header changed")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["fullSync"] != "true" {
				t.Errorf("payload changed: %#v", body)
			}
			_, _ = w.Write([]byte(`{"authMethod":"api-key"}`))
		case "/bottest/sendMessage":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["text"] != "alert" || body["chat_id"] != "chat" || body["parse_mode"] != nil {
				t.Error("Telegram payload changed")
			}
			if body["link_preview_options"].(map[string]any)["is_disabled"] != true {
				t.Error("previews enabled")
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/error":
			w.WriteHeader(503)
			_, _ = w.Write([]byte("private response"))
		case "/redirect":
			http.Redirect(w, r, "/target", 307)
		case "/target":
			t.Error("redirect followed")
		case "/invalid":
			_, _ = w.Write([]byte("not-json"))
		case "/large":
			_, _ = w.Write([]byte(strings.Repeat("x", 1_000_001)))
		case "/slow":
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(250 * time.Millisecond):
			}
		case "/botrejected/sendMessage":
			_, _ = w.Write([]byte(`{"ok":false}`))
		default:
			t.Error("unexpected endpoint")
		}
	}))
	defer server.Close()
	h := NewHTTP(Config{Timeout: time.Second, TelegramToken: "test", TelegramChat: "chat", TelegramURL: server.URL})
	j := domain.Job{URL: server.URL + "/sync", Authorization: "Bearer test-only", Body: []byte(`{"fullSync":"true"}`), Policy: "youtube-full"}
	if auth, err := h.Post(context.Background(), j); err != nil || auth.AuthMethod != "api-key" || auth.HTTPStatus != 200 {
		t.Fatalf("sync: %+v %v", auth, err)
	}
	if err := h.Send(context.Background(), "alert"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, code string
		status     int
	}{{"error", "HTTP_ERROR", 503}, {"redirect", "NETWORK_ERROR", 0}, {"invalid", "INVALID_JSON", 0}, {"large", "response_too_large", 0}, {"slow", "REQUEST_ABORTED", 0}} {
		t.Run(tc.path, func(t *testing.T) {
			before := count.Load()
			j.URL = server.URL + "/" + tc.path
			timeout := time.Second
			if tc.path == "slow" {
				timeout = 30 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			response, err := h.Post(ctx, j)
			var f *domain.Failure
			if !errors.As(err, &f) || f.Code != tc.code || f.Status != tc.status {
				t.Fatalf("wrong failure: %#v", err)
			}
			wantStatus := map[string]int{"error": 503, "redirect": 307, "invalid": 200, "large": 200, "slow": 0}[tc.path]
			if response.HTTPStatus != wantStatus {
				t.Fatalf("response status not retained: %d", response.HTTPStatus)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("leaked response")
			}
			if count.Load() != before+1 {
				t.Fatal("POST retried")
			}
		})
	}
	h.Token = "rejected"
	if err := h.Send(context.Background(), "alert"); err == nil {
		t.Fatal("rejected Telegram accepted")
	}
}

func TestPerJobTimeoutOverridesHeaderLimitAndBoundsBody(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		defaultTimeout time.Duration
		timeoutMS      int
		bodyDelay      bool
		wantFailure    bool
	}{
		{"longer than default", 100 * time.Millisecond, 2000, false, false},
		{"shorter than default", 2 * time.Second, 100, false, true},
		{"inherit default", 100 * time.Millisecond, 0, false, true},
		{"response body timeout", 2 * time.Second, 100, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tc.bodyDelay {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				select {
				case <-time.After(300 * time.Millisecond):
					_, _ = w.Write([]byte(`{}`))
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			h := NewHTTP(Config{Timeout: tc.defaultTimeout})
			_, err := h.Post(context.Background(), domain.Job{URL: server.URL, Body: []byte(`{}`), Policy: "generic", TimeoutMS: tc.timeoutMS})
			if tc.wantFailure {
				var failure *domain.Failure
				if !errors.As(err, &failure) || failure.Code != "REQUEST_ABORTED" {
					t.Fatalf("expected request timeout: %v", err)
				}
			} else if err != nil {
				t.Fatalf("longer task timeout was capped by the server default: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatal("timed out POST retried")
			}
		})
	}
}
