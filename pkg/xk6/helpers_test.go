package telegym

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.k6.io/k6/v2/js/modules"
	"go.k6.io/k6/v2/lib"
)

// fakeVU satisfies modules.VU with just enough behavior for the extension:
// only State() is called by our code. The embedded nil interface makes any
// other call panic loudly instead of silently returning zero values.
type fakeVU struct {
	modules.VU
	state *lib.State
}

func (v fakeVU) State() *lib.State { return v.state }

// fakeMock is a minimal stand-in for telegym-mock's debug API: it records
// every injected update and serves a fixed message list from
// /debug/messages/{token}.
type fakeMock struct {
	t *testing.T

	mu           sync.Mutex
	injected     []map[string]any
	queries      []string
	messages     []Message
	injectStatus int    // non-zero forces /debug/inject/update to fail
	rawMessages  string // non-empty replaces the JSON body of /debug/messages

	callbackID     string         // returned by /debug/inject/update when non-empty
	injectFields   map[string]any // merged into the /debug/inject/update response
	requests       []APIRequest
	requestQueries []string
	requestsStatus int // non-zero forces /debug/requests to fail
	cleared        int
}

func newFakeMock(t *testing.T) (*fakeMock, *Client) {
	t.Helper()
	m := &fakeMock{t: t}
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	t.Cleanup(srv.Close)

	t.Setenv("TELEGYM_MOCK_URL", srv.URL)
	t.Setenv("TELEGYM_BOT_TOKEN", "42:test")
	return m, NewClient()
}

func (m *fakeMock) serveHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/debug/inject/update":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			m.t.Errorf("inject: bad JSON body: %v", err)
		}
		m.injected = append(m.injected, body)
		if m.injectStatus != 0 {
			http.Error(w, "boom", m.injectStatus)
			return
		}
		response := map[string]any{"ok": true, "callback_query_id": m.callbackID}
		maps.Copy(response, m.injectFields)
		_ = json.NewEncoder(w).Encode(response)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/debug/requests/"):
		m.requestQueries = append(m.requestQueries, r.URL.RequestURI())
		if m.requestsStatus != 0 {
			http.Error(w, "boom", m.requestsStatus)
			return
		}
		method := r.URL.Query().Get("method")
		out := []APIRequest{}
		for _, request := range m.requests {
			if method == "" || request.Method == method {
				out = append(out, request)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"requests": out})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/debug/requests/") && strings.HasSuffix(r.URL.Path, "/clear"):
		_ = json.NewEncoder(w).Encode(map[string]any{"cleared": m.cleared})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/debug/messages/"):
		m.queries = append(m.queries, r.URL.RequestURI())
		if m.rawMessages != "" {
			_, _ = w.Write([]byte(m.rawMessages))
			return
		}
		_ = json.NewEncoder(w).Encode(messagesResp{Messages: m.messages})
	default:
		http.NotFound(w, r)
	}
}

func (m *fakeMock) setMessages(msgs ...Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = msgs
}

func (m *fakeMock) lastInjected() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.injected) == 0 {
		m.t.Fatal("nothing was injected")
	}
	return m.injected[len(m.injected)-1]
}

func (m *fakeMock) lastQuery() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queries) == 0 {
		m.t.Fatal("no /debug/messages request was made")
	}
	return m.queries[len(m.queries)-1]
}

// buttons builds a one-row inline keyboard from callback_data values.
func buttons(data ...string) *ReplyMarkup {
	row := make([]Button, 0, len(data))
	for _, d := range data {
		row = append(row, Button{Text: d, CallbackData: d})
	}
	return &ReplyMarkup{InlineKeyboard: [][]Button{row}}
}
