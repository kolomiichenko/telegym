package telegym

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func (m *fakeMock) setRequests(requests ...APIRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = requests
}

func journalEntry(seq int64, method string, parameters map[string]any) APIRequest {
	return APIRequest{Seq: seq, Method: method, Parameters: parameters, Status: 200, At: time.Now().Unix()}
}

func TestAwaitRequestMatchesIncludedParams(t *testing.T) {
	m, c := newFakeMock(t)
	u := c.NewUser(9, nil)
	m.setRequests(
		journalEntry(1, "getMe", map[string]any{}),
		journalEntry(2, "sendMessage", map[string]any{"chat_id": float64(9), "text": "plain"}),
		journalEntry(3, "sendMessage", map[string]any{"chat_id": float64(9), "text": "bold", "parse_mode": "HTML"}),
	)

	r, err := u.AwaitRequest("sendMessage", map[string]any{"parse_mode": "HTML"}, 1)
	if err != nil {
		t.Fatalf("AwaitRequest: %v", err)
	}
	if r.Seq != 3 || r.Status != 200 || r.LatencyMS < 0 {
		t.Errorf("request = %+v, want seq 3 with status 200", r)
	}
	q := m.requestQueries[len(m.requestQueries)-1]
	if !strings.HasPrefix(q, "/debug/requests/42:test?") || !strings.Contains(q, "method=sendMessage") {
		t.Errorf("poll query = %q", q)
	}
}

// TestAwaitRequestComparesAcrossEncodings: a form-encoded bot sends every
// parameter as a string, a JSON bot sends numbers and booleans.
func TestAwaitRequestComparesAcrossEncodings(t *testing.T) {
	m, c := newFakeMock(t)
	u := c.NewUser(9, nil)
	m.setRequests(journalEntry(1, "sendMessage", map[string]any{"chat_id": "9", "message_thread_id": "7", "protect_content": "true"}))

	params := map[string]any{"message_thread_id": int64(7), "protect_content": true}
	if _, err := u.AwaitRequest("sendMessage", params, 1); err != nil {
		t.Errorf("form-encoded values did not match typed params: %v", err)
	}
}

// TestAwaitRequestScopedToUser is the load-test contract: the journal is
// shared by all VUs, so one VU must never be satisfied by another's call.
func TestAwaitRequestScopedToUser(t *testing.T) {
	t.Run("other chat", func(t *testing.T) {
		m, c := newFakeMock(t)
		m.setRequests(journalEntry(1, "sendMessage", map[string]any{"chat_id": float64(1)}))
		if _, err := c.NewUser(2, nil).AwaitRequest("sendMessage", nil, 0.05); err == nil {
			t.Error("matched a call to another user's chat")
		}
	})
	t.Run("own and foreign clicks", func(t *testing.T) {
		m, c := newFakeMock(t)
		u := c.NewUser(1, nil)
		m.callbackID = "mine"
		if err := u.Click("btn"); err != nil {
			t.Fatalf("Click: %v", err)
		}

		m.setRequests(journalEntry(1, "answerCallbackQuery", map[string]any{"callback_query_id": "theirs"}))
		if _, err := u.AwaitRequest("answerCallbackQuery", nil, 0.05); err == nil {
			t.Error("matched an answer to another user's click")
		}

		m.setRequests(journalEntry(2, "answerCallbackQuery", map[string]any{"callback_query_id": "mine"}))
		if _, err := u.AwaitRequest("answerCallbackQuery", nil, 1); err != nil {
			t.Errorf("did not match the answer to this user's click: %v", err)
		}
	})
	t.Run("calls older than the user", func(t *testing.T) {
		m, c := newFakeMock(t)
		old := journalEntry(1, "sendMessage", map[string]any{"chat_id": float64(1)})
		old.At = time.Now().Unix() - 60
		m.setRequests(old)
		if _, err := c.NewUser(1, nil).AwaitRequest("sendMessage", nil, 0.05); err == nil {
			t.Error("matched a call from a previous iteration of the same chat")
		}
	})
}

func TestAwaitRequestReturnsEachCallOnce(t *testing.T) {
	m, c := newFakeMock(t)
	u := c.NewUser(1, nil)
	m.setRequests(
		journalEntry(1, "sendMessage", map[string]any{"chat_id": float64(1), "text": "a"}),
		journalEntry(2, "sendPhoto", map[string]any{"chat_id": float64(1)}),
		journalEntry(3, "sendMessage", map[string]any{"chat_id": float64(1), "text": "b"}),
	)

	// Awaiting the later method first must not hide the earlier one.
	if r, err := u.AwaitRequest("sendPhoto", nil, 1); err != nil || r.Seq != 2 {
		t.Fatalf("sendPhoto: %+v, %v", r, err)
	}
	for _, want := range []int64{1, 3} {
		r, err := u.AwaitRequest("sendMessage", nil, 1)
		if err != nil || r.Seq != want {
			t.Fatalf("sendMessage: %+v, %v; want seq %d", r, err, want)
		}
	}
	if _, err := u.AwaitRequest("sendMessage", nil, 0.05); err == nil {
		t.Error("returned an already-seen call")
	}
}

func TestAwaitRequestErrors(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		_, c := newFakeMock(t)
		_, err := c.NewUser(5, nil).AwaitRequest("sendMessage", nil, 0.05)
		if err == nil || !strings.Contains(err.Error(), "timeout") ||
			!strings.Contains(err.Error(), "chat=5") || !strings.Contains(err.Error(), "method=sendMessage") {
			t.Errorf("err = %v, want timeout naming the chat and method", err)
		}
	})
	t.Run("non-2xx status", func(t *testing.T) {
		m, c := newFakeMock(t)
		m.requestsStatus = http.StatusInternalServerError
		_, err := c.NewUser(1, nil).AwaitRequest("sendMessage", nil, 1)
		if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "boom") {
			t.Errorf("err = %v, want status 500 with response body", err)
		}
	})
}

func TestAPIRequests(t *testing.T) {
	m, c := newFakeMock(t)
	api := &API{client: c}
	m.setRequests(journalEntry(1, "getMe", nil), journalEntry(2, "sendMessage", nil))

	all, err := api.Requests("", 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("Requests: %+v, %v", all, err)
	}
	if q := m.requestQueries[0]; strings.Contains(q, "method=") || strings.Contains(q, "limit=") {
		t.Errorf("unfiltered query = %q", q)
	}

	filtered, err := api.Requests("sendMessage", 7)
	if err != nil || len(filtered) != 1 || filtered[0].Seq != 2 {
		t.Fatalf("Requests(sendMessage): %+v, %v", filtered, err)
	}
	if q := m.requestQueries[1]; !strings.Contains(q, "method=sendMessage") || !strings.Contains(q, "limit=7") {
		t.Errorf("filtered query = %q", q)
	}
}

func TestAPIClearRequests(t *testing.T) {
	m, c := newFakeMock(t)
	m.cleared = 3
	if n, err := (&API{client: c}).ClearRequests(); err != nil || n != 3 {
		t.Errorf("ClearRequests = %d, %v; want 3", n, err)
	}
}
