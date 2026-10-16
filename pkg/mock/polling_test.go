package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func pollingRequest(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeUpdates(t *testing.T, rec *httptest.ResponseRecorder) []Update {
	t.Helper()
	var response struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode getUpdates: %v", err)
	}
	if rec.Code != http.StatusOK || !response.OK || response.Result == nil {
		t.Fatalf("getUpdates status=%d body=%s", rec.Code, rec.Body.String())
	}
	return response.Result
}

func queueText(t *testing.T, srv *Server, token, text string) {
	t.Helper()
	body := fmt.Sprintf(`{"token":%q,"chat_id":42,"text":%q}`, token, text)
	rec := pollingRequest(srv, http.MethodPost, "/debug/inject/update", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("inject status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPollingOffsetsAndIsolation(t *testing.T) {
	for _, prefix := range []string{"/bot888:poll", "/bot888:poll/test"} {
		t.Run(prefix, func(t *testing.T) {
			srv := New(Config{Quiet: true})
			queueText(t, srv, "888:poll", "first")
			queueText(t, srv, "888:poll", "second")
			queueText(t, srv, "999:other", "other bot")
			rec := pollingRequest(srv, http.MethodPost, "/debug/inject/update", `{"token":"888:poll","chat_id":42,"callback_data":"next","message_id":12}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("inject callback status=%d body=%s", rec.Code, rec.Body.String())
			}

			updates := decodeUpdates(t, pollingRequest(srv, http.MethodGet, prefix+"/getUpdates?limit=1", ""))
			if len(updates) != 1 || updates[0].Message == nil || updates[0].Message.Text != "first" {
				t.Fatalf("unexpected first batch: %+v", updates)
			}
			firstID := updates[0].UpdateID

			// Reading an update does not acknowledge it; it remains pending.
			updates = decodeUpdates(t, pollingRequest(srv, http.MethodGet, prefix+"/getUpdates?limit=1", ""))
			if len(updates) != 1 || updates[0].UpdateID != firstID {
				t.Fatalf("polling acknowledged an update without an offset: %+v", updates)
			}

			// An offset above the first update's ID acknowledges it.
			body := fmt.Sprintf(`{"offset":%d,"limit":1}`, firstID+1)
			updates = decodeUpdates(t, pollingRequest(srv, http.MethodPost, prefix+"/getUpdates", body))
			if len(updates) != 1 || updates[0].Message == nil || updates[0].Message.Text != "second" {
				t.Fatalf("unexpected acknowledged batch: %+v", updates)
			}

			// A negative offset counts from the end and discards older updates.
			updates = decodeUpdates(t, pollingRequest(srv, http.MethodGet, prefix+"/getUpdates?offset=-1", ""))
			if len(updates) != 1 || updates[0].CallbackQuery == nil || updates[0].CallbackQuery.Data != "next" {
				t.Fatalf("unexpected negative-offset batch: %+v", updates)
			}
			lastID := updates[0].UpdateID

			// Even the smallest int64 offset must preserve the remaining update.
			updates = decodeUpdates(t, pollingRequest(srv, http.MethodGet, prefix+"/getUpdates?offset=-9223372036854775808", ""))
			if len(updates) != 1 || updates[0].UpdateID != lastID {
				t.Fatalf("large negative offset lost pending updates: %+v", updates)
			}

			// Acknowledge the last update using a form request, leaving no backlog.
			rec = httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, prefix+"/getUpdates", strings.NewReader(fmt.Sprintf("offset=%d", lastID+1)))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			srv.Handler().ServeHTTP(rec, req)
			if updates = decodeUpdates(t, rec); len(updates) != 0 {
				t.Fatalf("acknowledged updates remain: %+v", updates)
			}

			// Acknowledgments apply only to the bot being polled.
			updates = decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot999:other/getUpdates", ""))
			if len(updates) != 1 || updates[0].Message == nil || updates[0].Message.Text != "other bot" {
				t.Fatalf("other bot queue changed: %+v", updates)
			}
		})
	}
}

func TestPollingWebhookLifecycle(t *testing.T) {
	received := make(chan Update, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var update Update
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- update
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	for _, defaultURL := range []string{"", receiver.URL} {
		t.Run("default="+defaultURL, func(t *testing.T) {
			srv := New(Config{Quiet: true, DefaultWebhookURL: defaultURL})
			// A configured default webhook also prevents polling.
			if defaultURL != "" {
				rec := pollingRequest(srv, http.MethodGet, "/bot888:poll/getUpdates", "")
				if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"ok":false`) {
					t.Fatalf("polling with default webhook status=%d body=%s, want 409", rec.Code, rec.Body.String())
				}
				queueText(t, srv, "888:poll", "default webhook")
				select {
				case <-received:
				default:
					t.Fatal("configured default did not receive update")
				}
			}

			// Deleting the webhook overrides the default and enables polling.
			rec := pollingRequest(srv, http.MethodPost, "/bot888:poll/deleteWebhook", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("deleteWebhook status=%d body=%s", rec.Code, rec.Body.String())
			}
			queueText(t, srv, "888:poll", "pending")

			// Registering a webhook blocks polling and delivers new updates by HTTP.
			body := fmt.Sprintf(`{"url":%q}`, receiver.URL)
			rec = pollingRequest(srv, http.MethodPost, "/bot888:poll/setWebhook", body)
			if rec.Code != http.StatusOK {
				t.Fatalf("setWebhook status=%d body=%s", rec.Code, rec.Body.String())
			}
			rec = pollingRequest(srv, http.MethodGet, "/bot888:poll/getUpdates", "")
			if rec.Code != http.StatusConflict {
				t.Fatalf("polling with webhook status=%d, want 409", rec.Code)
			}
			queueText(t, srv, "888:poll", "webhook")
			select {
			case update := <-received:
				if update.Message == nil || update.Message.Text != "webhook" {
					t.Fatalf("unexpected webhook update: %+v", update)
				}
			default:
				t.Fatal("registered webhook did not receive update")
			}

			// The queued update is still available when polling resumes.
			pollingRequest(srv, http.MethodPost, "/bot888:poll/deleteWebhook", "")
			updates := decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot888:poll/getUpdates", ""))
			if len(updates) != 1 || updates[0].Message == nil || updates[0].Message.Text != "pending" {
				t.Fatalf("webhook switch lost pending updates: %+v", updates)
			}

			// Both webhook methods can explicitly discard the polling backlog.
			for _, method := range []string{"setWebhook", "deleteWebhook"} {
				pollingRequest(srv, http.MethodPost, "/bot888:poll/"+method, `{"drop_pending_updates":true}`)
				updates = decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot888:poll/getUpdates", ""))
				if len(updates) != 0 {
					t.Fatalf("%s did not drop pending updates: %+v", method, updates)
				}
				queueText(t, srv, "888:poll", "pending")
			}
		})
	}
}

func TestPollingWaitsForUpdateAndCancels(t *testing.T) {
	srv := New(Config{Quiet: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/bot888:poll/getUpdates?timeout=30", nil).WithContext(ctx)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		done <- rec
	}()
	select {
	case rec := <-done:
		t.Fatalf("empty long poll returned immediately: %s", rec.Body.String())
	case <-time.After(50 * time.Millisecond):
	}

	// An arriving update should finish the poll before its timeout.
	queueText(t, srv, "888:poll", "wake up")
	select {
	case rec := <-done:
		updates := decodeUpdates(t, rec)
		if len(updates) != 1 || updates[0].Message == nil || updates[0].Message.Text != "wake up" {
			t.Fatalf("unexpected long-poll batch: %+v", updates)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long poll did not wake after injection")
	}

	// A separate token has an empty queue, so this request waits for cancellation.
	req = httptest.NewRequest(http.MethodGet, "/bot999:other/getUpdates?timeout=30", nil).WithContext(ctx)
	go func() {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		done <- rec
	}()
	select {
	case rec := <-done:
		t.Fatalf("empty long poll returned before cancellation: %s", rec.Body.String())
	case <-time.After(50 * time.Millisecond):
	}

	// Only one poll may wait for updates for a given bot.
	rec := pollingRequest(srv, http.MethodGet, "/bot999:other/getUpdates", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("concurrent poll status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	cancel()
	select {
	case rec := <-done:
		if updates := decodeUpdates(t, rec); len(updates) != 0 {
			t.Fatalf("cancelled empty poll returned updates: %+v", updates)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled poll did not return")
	}

	// Cancellation must release the bot so another poll can proceed.
	decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot999:other/getUpdates", ""))
}

func TestPollingTimeout(t *testing.T) {
	srv := New(Config{Quiet: true})
	start := time.Now()
	updates := decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot888:poll/getUpdates?timeout=1", ""))
	if len(updates) != 0 || time.Since(start) < time.Second {
		t.Fatalf("empty poll did not wait for timeout: updates=%+v elapsed=%s", updates, time.Since(start))
	}
}

func TestPollingDebugChat(t *testing.T) {
	srv := New(Config{Quiet: true})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/chat/888:poll/42/send", strings.NewReader("text=hello&name=Alice"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("chat send status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = pollingRequest(srv, http.MethodPost, "/debug/chat/888:poll/42/click", `{"data":"next","message_id":12}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("chat click status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Chat UI messages and button clicks enter the same polling queue.
	updates := decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot888:poll/getUpdates", ""))
	if len(updates) != 2 || updates[0].Message == nil || updates[0].Message.Text != "hello" ||
		updates[1].CallbackQuery == nil || updates[1].CallbackQuery.Data != "next" {
		t.Fatalf("unexpected debug chat updates: %+v", updates)
	}
}

func TestPollingQueueRetention(t *testing.T) {
	srv := New(Config{Quiet: true})
	// Exceed the queue capacity by one; the oldest update should be discarded.
	for i := 0; i <= 5000; i++ {
		queueText(t, srv, "888:poll", fmt.Sprint(i))
	}
	updates := decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot888:poll/getUpdates", ""))
	if len(updates) != 100 || updates[0].Message == nil || updates[0].Message.Text != "1" {
		t.Fatalf("unexpected retained batch: %+v", updates)
	}

	// Reading a batch does not reduce the pending count without acknowledgment.
	rec := pollingRequest(srv, http.MethodGet, "/bot888:poll/getWebhookInfo", "")
	var info struct {
		Result WebhookInfo `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode webhook info: %v", err)
	}
	if rec.Code != http.StatusOK || info.Result.URL != "" || info.Result.PendingUpdateCount != 5000 {
		t.Fatalf("unexpected pending queue info: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
