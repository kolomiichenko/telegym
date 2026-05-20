package mock

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMockURL covers the bind-address normalization used by the startup
// card. `:5678` and `0.0.0.0:5678` are the two forms users hit in
// practice (default flag value and docker-compose), both must collapse
// to a clickable `http://localhost:5678`.
func TestMockURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{":5678", "http://localhost:5678"},
		{":80", "http://localhost:80"},
		{"0.0.0.0:5678", "http://localhost:5678"},
		{"0.0.0.0:9104", "http://localhost:9104"},
		{"127.0.0.1:5678", "http://127.0.0.1:5678"},
		{"example.com:8080", "http://example.com:8080"},
	}
	for _, c := range cases {
		if got := mockURL(c.in); got != c.want {
			t.Errorf("mockURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestParseBotPath exercises the URL parser the NoRoute catch-all uses to
// resolve `/bot<token>/[test/]<method>` requests. Failure modes here mean
// the mock returns a generic 404 to clients that would otherwise hit the
// spec-driven dispatcher, so the parser's negative cases matter.
func TestParseBotPath(t *testing.T) {
	type result struct {
		token, method string
		ok            bool
	}
	cases := []struct {
		path string
		want result
	}{
		{"/bot123:abc/sendMessage", result{"123:abc", "sendMessage", true}},
		{"/bot123:abc/test/sendMessage", result{"123:abc", "sendMessage", true}},
		{"/bot42:xyz/getChat", result{"42:xyz", "getChat", true}},
		// Edge cases that must NOT parse:
		{"/bot123:abc", result{"", "", false}},       // no method
		{"/bot/sendMessage", result{"", "", false}},  // empty token segment
		{"/bot123:abc/", result{"", "", false}},      // trailing slash, no method
		{"/bot123:abc/test/", result{"", "", false}}, // /test/ with no method
	}
	for _, c := range cases {
		token, method, ok := parseBotPath(c.path)
		if token != c.want.token || method != c.want.method || ok != c.want.ok {
			t.Errorf("parseBotPath(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.path, token, method, ok, c.want.token, c.want.method, c.want.ok)
		}
	}
}

// TestHealthEndpoint is an end-to-end smoke that the gin router answers
// /health with the expected JSON. Useful as a regression canary -- if
// routes get accidentally reshuffled, this is the first thing that
// breaks.
func TestHealthEndpoint(t *testing.T) {
	srv := New(Config{Quiet: true})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v -- body=%s", err, rec.Body.String())
	}
	if body["status"] != "ok" {
		t.Errorf("body[status] = %q, want %q", body["status"], "ok")
	}
}

// TestGetMeEndpoint is the cheapest happy-path through the real Bot API
// surface: the bot auto-registers on first hit, GetMe returns a stable
// identity. If this breaks, every load-test scenario breaks too.
func TestGetMeEndpoint(t *testing.T) {
	srv := New(Config{Quiet: true})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bot42:test_token/getMe", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v -- body=%s", err, rec.Body.String())
	}
	if !resp.OK {
		t.Errorf("APIResponse.OK = false, want true; description=%q", resp.Description)
	}
	if resp.Result == nil {
		t.Error("APIResponse.Result is nil, want non-empty User identity")
	}
}

// TestGetMeUnderTestPrefix confirms the /test/ prefix variant (used by
// clients with WithTestServerPath like telego) routes to the same
// handler.
func TestGetMeUnderTestPrefix(t *testing.T) {
	srv := New(Config{Quiet: true})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bot42:test_token/test/getMe", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Errorf("expected ok:true in body, got %s", rec.Body.String())
	}
}

// TestSendMessageRoundtrip is the #1 happy path every k6 scenario depends
// on: bot calls sendMessage, the mock persists it, /debug/messages
// returns it. If this regresses every scenario silently breaks, since
// awaits poll /debug/messages until a match is found.
func TestSendMessageRoundtrip(t *testing.T) {
	srv := New(Config{Quiet: true})
	const token = "555:roundtrip_token"
	const chatID = 42

	// 1) bot sends a message
	body := bytes.NewBufferString(`{"chat_id":42,"text":"hello from bot"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bot"+token+"/sendMessage", body)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("sendMessage status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var sendResp struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int    `json:"message_id"`
			Text      string `json:"text"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sendResp); err != nil {
		t.Fatalf("decode sendMessage response: %v", err)
	}
	if !sendResp.OK || sendResp.Result.MessageID == 0 || sendResp.Result.Text != "hello from bot" {
		t.Fatalf("unexpected sendMessage response: %+v", sendResp)
	}

	// 2) /debug/messages must surface the same message
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/debug/messages/"+token+"?chat_id=42", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("debug/messages status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Response shape: {"messages": [ {message_id, text, ...}, ... ]}
	var listResp struct {
		Messages []struct {
			MessageID int    `json:"message_id"`
			Text      string `json:"text"`
			Chat      struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode debug/messages response: %v -- body=%s", err, rec.Body.String())
	}
	if len(listResp.Messages) != 1 {
		t.Fatalf("debug/messages returned %d entries, want 1: %s", len(listResp.Messages), rec.Body.String())
	}
	got := listResp.Messages[0]
	if got.Text != "hello from bot" || got.Chat.ID != chatID || got.MessageID != sendResp.Result.MessageID {
		t.Errorf("debug/messages entry mismatch: got %+v, want text=%q chat_id=%d message_id=%d",
			got, "hello from bot", chatID, sendResp.Result.MessageID)
	}
}

// TestInjectUpdateFiresWebhook is the OTHER half of the roundtrip: k6
// injects an update via /debug/inject/update, the mock POSTs it to the
// bot's webhook (here a httptest receiver). Every load-test scenario
// stands or falls on this path -- if the webhook never fires, the bot
// under test never receives messages and the run is silently a no-op.
func TestInjectUpdateFiresWebhook(t *testing.T) {
	const token = "777:webhook_token"

	// Receiver captures the dispatched update body. Buffered so the
	// webhook dispatch goroutine doesn't block if the test is slow.
	received := make(chan []byte, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case received <- body:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	// DefaultWebhookURL applies to any bot that hasn't called setWebhook.
	srv := New(Config{Quiet: true, DefaultWebhookURL: receiver.URL})

	// Inject a message-shaped update.
	payload := `{"token":"` + token + `","chat_id":99,"user_id":99,"first_name":"Test","text":"ping"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/inject/update", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inject status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Wait for the dispatch goroutine to deliver. 2s is generous --
	// local httptest dispatch is usually sub-millisecond.
	select {
	case body := <-received:
		if !bytes.Contains(body, []byte(`"ping"`)) {
			t.Errorf("webhook body missing injected text %q: %s", "ping", string(body))
		}
		if !bytes.Contains(body, []byte(`"chat":{"id":99`)) && !bytes.Contains(body, []byte(`"chat_id":99`)) {
			t.Errorf("webhook body missing chat_id 99: %s", string(body))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook never fired within 2s")
	}
}
