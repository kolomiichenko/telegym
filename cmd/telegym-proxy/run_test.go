package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/kolomiichenko/telegym/pkg/mock"
)

func TestParseConfig(t *testing.T) {
	t.Setenv("PROXY_TOKEN", "env-token")
	t.Setenv("MOCK_URL", "http://mock:5678")
	t.Setenv("ALLOWED_USER_ID", "7")
	t.Setenv("TELEGYM_PROXY_LISTEN", "127.0.0.1:9000")

	c, done, err := parseConfig(nil, io.Discard)
	if err != nil || done {
		t.Fatalf("done=%t err=%v", done, err)
	}
	want := config{
		proxyToken:    "env-token",
		mockURL:       "http://mock:5678",
		mockBotToken:  testMockToken,
		listen:        "127.0.0.1:9000",
		publicURL:     "http://127.0.0.1:9000",
		allowedUserID: 7,
		pollTimeout:   30,
	}
	if c != want {
		t.Fatalf("cfg = %+v, want %+v", c, want)
	}

	// Flags win over env; an explicit public URL is kept as is.
	c, _, err = parseConfig([]string{"-proxy-token", "flag-token", "-allowed-user-id", "0", "-public-url", "http://pub"}, io.Discard)
	if err != nil || c.proxyToken != "flag-token" || c.allowedUserID != 0 || c.publicURL != "http://pub" {
		t.Fatalf("cfg = %+v, err = %v", c, err)
	}

	var out strings.Builder
	if _, done, err := parseConfig([]string{"-version"}, &out); err != nil || !done || out.String() != version+"\n" {
		t.Fatalf("-version: done=%t err=%v out=%q", done, err, out.String())
	}

	t.Setenv("PROXY_TOKEN", "")
	if _, _, err := parseConfig(nil, io.Discard); err == nil || !strings.Contains(err.Error(), "PROXY_TOKEN") {
		t.Fatalf("missing token: err = %v", err)
	}
}

// pollingTelegram is a fake real Telegram for run: getUpdates hands out
// updates once, then long-polls until the client gives up.
func pollingTelegram(t *testing.T, updates string) (*httptest.Server, <-chan string) {
	t.Helper()
	calls := make(chan string, 16)
	var served atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		calls <- method
		result := `true`
		switch method {
		case "getMe":
			result = `{"id":1,"is_bot":true,"first_name":"proxy","username":"proxy_bot"}`
		case "getUpdates":
			result = updates
			if served.Swap(true) {
				// An empty long poll; telego can't abort one in flight.
				time.Sleep(100 * time.Millisecond)
				result = `[]`
			}
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":`+result+`}`)
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func TestRunRelaysPolledUpdates(t *testing.T) {
	mockSrv := httptest.NewServer(mock.New(mock.Config{Quiet: true}).Handler())
	t.Cleanup(mockSrv.Close)
	botCall(t, mockSrv.URL, "deleteWebhook", `{}`) // the bot-under-test long-polls

	tg, calls := pollingTelegram(t, fmt.Sprintf(
		`[{"update_id":10,"message":{"message_id":1,"date":1,"chat":{"id":%d,"type":"private"},"from":{"id":%d,"is_bot":false,"first_name":"dev"},"text":"via run"}}]`,
		testChatID, testChatID))
	cfg := config{
		proxyToken:    testProxyToken,
		mockURL:       mockSrv.URL,
		mockBotToken:  testMockToken,
		listen:        "127.0.0.1:0",
		publicURL:     "http://127.0.0.1:1",
		allowedUserID: testChatID,
		pollTimeout:   1,
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- run(ctx, cfg, telego.WithAPIServer(tg.URL), telego.WithDiscardLogger()) }()

	var updates []map[string]any
	for deadline := time.Now().Add(5 * time.Second); len(updates) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("polled update never reached the bot-under-test")
		}
		_ = json.Unmarshal(botCall(t, mockSrv.URL, "getUpdates", `{"timeout":0}`), &updates)
		time.Sleep(20 * time.Millisecond)
	}
	if msg, _ := updates[0]["message"].(map[string]any); msg == nil || msg["text"] != "via run" {
		t.Fatalf("bot-under-test got %v", updates)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop on cancel")
	}
	var got []string
	for len(calls) > 0 {
		got = append(got, <-calls)
	}
	if len(got) < 3 || got[0] != "getMe" || got[1] != "deleteWebhook" || got[2] != "getUpdates" {
		t.Fatalf("Telegram calls = %v, want getMe, deleteWebhook, getUpdates...", got)
	}
}

func TestRunFailsFast(t *testing.T) {
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	}))
	t.Cleanup(tg.Close)
	opts := []telego.BotOption{telego.WithAPIServer(tg.URL), telego.WithDiscardLogger()}

	if err := run(context.Background(), config{proxyToken: "bad"}, opts...); err == nil || !strings.Contains(err.Error(), "NewBot") {
		t.Errorf("malformed token: err = %v", err)
	}
	if err := run(context.Background(), config{proxyToken: testProxyToken}, opts...); err == nil || !strings.Contains(err.Error(), "getMe") {
		t.Errorf("rejected token: err = %v", err)
	}

	ok, _ := pollingTelegram(t, `[]`)
	err := run(context.Background(), config{proxyToken: testProxyToken, listen: "256.0.0.1:x"},
		telego.WithAPIServer(ok.URL), telego.WithDiscardLogger())
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Errorf("bad listen address: err = %v", err)
	}
}

func TestPollLoopRetriesAfterErrors(t *testing.T) {
	var n atomic.Int32
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
	}))
	t.Cleanup(tg.Close)
	bot, _ := telego.NewBot(testProxyToken, telego.WithAPIServer(tg.URL), telego.WithDiscardLogger())
	p := &proxy{bot: bot}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.pollLoop(ctx); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); n.Load() < 2; {
		if time.Now().After(deadline) {
			t.Fatal("pollLoop did not retry after an error")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pollLoop did not stop on cancel")
	}
}

func TestMockUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	p := &proxy{cfg: config{mockURL: dead.URL}, httpc: &http.Client{Timeout: time.Second}}

	p.bindIfNeeded(testChatID) // logs and carries on
	if _, ok := p.inject(map[string]any{}); ok {
		t.Fatal("inject succeeded with the mock down")
	}
	if _, _, _, err := p.fetchFile("f"); err == nil {
		t.Fatal("fetchFile succeeded with the mock down")
	}
}

func TestRelayMediaKinds(t *testing.T) {
	ctx := context.Background()
	p, _, calls := testStand(t)
	// A file store without Content-Disposition: the proxy names the upload itself.
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("bytes"))
	}))
	t.Cleanup(files.Close)
	p.cfg.mockURL = files.URL
	m := telego.Message{Chat: telego.Chat{ID: testChatID}}

	for kind, method := range map[string]string{"video": "sendVideo", "animation": "sendAnimation"} {
		if err := p.relayMedia(ctx, m, kind, "f", "cap", nil); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if c := waitCall(t, calls, method); c.params[kind] != kind+".bin" || c.params["caption"] != "cap" {
			t.Fatalf("%s params: %v", method, c.params)
		}
	}
	if err := p.relayMedia(ctx, m, "voice", "f", "", nil); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("voice: err = %v", err)
	}
}
