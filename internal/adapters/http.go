package adapters

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"github.com/fixtse/SchedulerSE/internal/domain"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type HTTP struct {
	Client                   *http.Client
	Token, Chat, TelegramURL string
	RequestTimeout           time.Duration
	EnableBash               bool
}

func NewHTTP(c Config) *HTTP {
	// No pooled connections or HTTP/2 stream replays for side-effecting requests.
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, DisableKeepAlives: true, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}}
	return &HTTP{Client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Token: c.TelegramToken, Chat: c.TelegramChat, TelegramURL: c.TelegramURL, RequestTimeout: c.Timeout, EnableBash: c.EnableBash}
}
func (h *HTTP) post(ctx context.Context, endpoint, auth string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, &domain.Failure{Code: "NETWORK_ERROR"}
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	res, err := h.Client.Do(req)
	if err != nil {
		code := "NETWORK_ERROR"
		if ctx.Err() != nil {
			code = "REQUEST_ABORTED"
		}
		return nil, 0, &domain.Failure{Code: code}
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return nil, res.StatusCode, &domain.Failure{Code: "NETWORK_ERROR"}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, res.StatusCode, &domain.Failure{Code: "HTTP_ERROR", Status: res.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 1_000_001))
	if len(data) > 1_000_000 {
		return nil, res.StatusCode, &domain.Failure{Code: "response_too_large"}
	}
	if err != nil {
		code := "NETWORK_ERROR"
		if ctx.Err() != nil {
			code = "REQUEST_ABORTED"
		}
		return nil, res.StatusCode, &domain.Failure{Code: code}
	}
	return data, res.StatusCode, nil
}
func (h *HTTP) Post(ctx context.Context, j domain.Job) (domain.Response, error) {
	if j.Kind == "bash" {
		if !h.EnableBash {
			return domain.Response{}, &domain.Failure{Code: "SCRIPT_DISABLED"}
		}
		return runScript(ctx, j)
	}
	timeout := h.RequestTimeout
	if j.TimeoutMS > 0 {
		timeout = time.Duration(j.TimeoutMS) * time.Millisecond
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	data, status, err := h.post(ctx, j.URL, j.Authorization, j.Body)
	response := domain.Response{HTTPStatus: status}
	if err != nil {
		return response, err
	}
	if !domain.IsYouTube(j) {
		return response, nil
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return response, &domain.Failure{Code: "INVALID_JSON"}
	}
	if obj, ok := value.(map[string]any); ok {
		s, _ := obj["authMethod"].(string)
		response.AuthMethod = s
		return response, nil
	}
	return response, nil
}
func (h *HTTP) Send(ctx context.Context, text string) error {
	if h.Token == "" || h.Chat == "" {
		return &domain.Failure{Code: "TELEGRAM_NOT_CONFIGURED"}
	}
	body, _ := json.Marshal(map[string]any{"chat_id": h.Chat, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}})
	data, _, err := h.post(ctx, strings.TrimRight(h.TelegramURL, "/")+"/bot"+h.Token+"/sendMessage", "", body)
	if err != nil {
		return err
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(data, &result) != nil || !result.OK {
		return &domain.Failure{Code: "TELEGRAM_REJECTED"}
	}
	return nil
}
