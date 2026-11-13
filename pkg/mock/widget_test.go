package mock

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const widgetBot = "741:widget"

func widgetCall(srv *Server, method, body string) *httptest.ResponseRecorder {
	return pollingRequest(srv, http.MethodPost, "/bot"+widgetBot+"/"+method, body)
}

// sendTestWidget sends a widget to chat 42 and returns the sent message.
func sendTestWidget(t *testing.T, srv *Server, fields string) Message {
	t.Helper()
	rec := widgetCall(srv, "sendWidget", `{"chat_id":42,"html":"<b>hi</b>"`+fields+`}`)
	var env struct {
		Result Message `json:"result"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &env) != nil || env.Result.Widget == nil {
		t.Fatalf("sendWidget status=%d body=%s", rec.Code, rec.Body.String())
	}
	return env.Result
}

func injectWidgetQuery(srv *Server, fields string) *httptest.ResponseRecorder {
	return pollingRequest(srv, http.MethodPost, "/debug/inject/update",
		`{"token":"`+widgetBot+`","chat_id":42,`+fields+`}`)
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status=%d want %d body=%s", rec.Code, status, rec.Body.String())
	}
}

func TestSendWidgetValidation(t *testing.T) {
	random := make([]byte, 6000)
	_, _ = rand.Read(random)
	incompressible := base64.StdEncoding.EncodeToString(random)
	long := strings.Repeat("a", maxWidgetHTML+1)
	data := strings.Repeat("d", maxWidgetData)

	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"minimal", `{"chat_id":42,"html":"<b>hi</b>"}`, http.StatusOK},
		{"data at limits", `{"chat_id":42,"html":"x","public_data":"` + data + `","chat_data":"` + data + `"}`, http.StatusOK},
		{"html at limit", `{"chat_id":42,"html":"` + strings.Repeat("a", maxWidgetHTML) + `"}`, http.StatusOK},
		{"missing html", `{"chat_id":42}`, http.StatusBadRequest},
		{"html too long", `{"chat_id":42,"html":"` + long + `"}`, http.StatusBadRequest},
		{"html too large compressed", `{"chat_id":42,"html":"` + incompressible + `"}`, http.StatusBadRequest},
		{"public_data too long", `{"chat_id":42,"html":"x","public_data":"` + data + `d"}`, http.StatusBadRequest},
		{"chat_data too long", `{"chat_id":42,"html":"x","chat_data":"` + data + `d"}`, http.StatusBadRequest},
		{"caption at limit", `{"chat_id":42,"html":"x","caption":"` + strings.Repeat("я", maxWidgetCaption) + `"}`, http.StatusOK},
		{"caption too long", `{"chat_id":42,"html":"x","caption":"` + strings.Repeat("a", maxWidgetCaption+1) + `"}`, http.StatusBadRequest},
		{"missing chat_id", `{"html":"x"}`, http.StatusBadRequest},
		{"group", `{"chat_id":-42,"html":"x"}`, http.StatusBadRequest},
		{"channel", `{"chat_id":-1001234567890,"html":"x"}`, http.StatusBadRequest},
		{"username", `{"chat_id":"@channel","html":"x"}`, http.StatusBadRequest},
		{"malformed reply parameters", `{"chat_id":42,"html":"x","reply_parameters":"{"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(Config{Quiet: true})
			wantStatus(t, widgetCall(srv, "sendWidget", tt.body), tt.status)
		})
	}
}

func TestSendWidgetResult(t *testing.T) {
	srv := New(Config{Quiet: true})
	m := sendTestWidget(t, srv, `,"width":300,"height":200,"public_data":"p","chat_data":"c","caption":"cap","allow_queries":true`)
	w := m.Widget
	if w.HTML != "<b>hi</b>" || w.Width != 300 || w.Height != 200 || w.PublicData != "p" || w.ChatData != "c" {
		t.Fatalf("unexpected widget: %+v", w)
	}
	if w.ID != "" {
		t.Fatalf("non-shared widget has id %q", w.ID)
	}
	if !m.HasProtectedContent || m.Caption != "cap" {
		t.Fatalf("unexpected message: %+v", m)
	}
	if stored := srv.Store().Bot(widgetBot).Messages(42, 1); len(stored) != 1 || stored[0].Widget == nil || !stored[0].Widget.AllowQueries {
		t.Fatalf("widget not stored with allow_queries: %+v", stored)
	}
}

func TestSendWidgetForm(t *testing.T) {
	srv := New(Config{Quiet: true})
	form := url.Values{"chat_id": {"42"}, "html": {"x"}, "is_shared": {"true"}, "allow_queries": {"true"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/bot"+widgetBot+"/sendWidget", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	wantStatus(t, rec, http.StatusOK)
	stored := srv.Store().Bot(widgetBot).Messages(42, 1)
	if len(stored) != 1 || stored[0].Widget.ID == "" || !stored[0].Widget.AllowQueries {
		t.Fatalf("form booleans ignored: %+v", stored[0].Widget)
	}
}

func TestSendSharedWidget(t *testing.T) {
	srv := New(Config{Quiet: true})
	first := sendTestWidget(t, srv, `,"is_shared":true,"public_data":"p","chat_data":"first"`)
	id := first.Widget.ID
	if id == "" {
		t.Fatal("shared widget has no id")
	}

	rec := widgetCall(srv, "sendSharedWidget", `{"chat_id":43,"widget_id":"`+id+`","chat_data":"second"}`)
	wantStatus(t, rec, http.StatusOK)
	var env struct {
		Result Message `json:"result"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	w := env.Result.Widget
	if w == nil || w.ID != id || w.HTML != "<b>hi</b>" || w.PublicData != "p" || w.ChatData != "second" {
		t.Fatalf("unexpected shared widget: %+v", w)
	}

	for name, body := range map[string]string{
		"unknown widget":    `{"chat_id":42,"widget_id":"nope"}`,
		"missing widget_id": `{"chat_id":42}`,
		"group":             `{"chat_id":-42,"widget_id":"` + id + `"}`,
		"caption too long":  `{"chat_id":42,"widget_id":"` + id + `","caption":"` + strings.Repeat("a", maxWidgetCaption+1) + `"}`,
		"chat_data too long": `{"chat_id":42,"widget_id":"` + id + `","chat_data":"` +
			strings.Repeat("d", maxWidgetData+1) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, widgetCall(srv, "sendSharedWidget", body), http.StatusBadRequest)
		})
	}
	t.Run("another bot", func(t *testing.T) {
		rec := pollingRequest(srv, http.MethodPost, "/bot742:other/sendSharedWidget", `{"chat_id":42,"widget_id":"`+id+`"}`)
		wantStatus(t, rec, http.StatusBadRequest)
	})
}

func TestEditMessageWidgetData(t *testing.T) {
	srv := New(Config{Quiet: true})
	m := sendTestWidget(t, srv, `,"public_data":"p","chat_data":"c"`)
	edit := func(fields string) *httptest.ResponseRecorder {
		return widgetCall(srv, "editMessageWidgetData", fmt.Sprintf(`{"chat_id":42,"message_id":%d%s}`, m.MessageID, fields))
	}
	latest := func() *Widget {
		return srv.Store().Bot(widgetBot).Messages(42, 1)[0].Widget
	}

	wantStatus(t, edit(`,"chat_data":"c2"`), http.StatusOK)
	if w := latest(); w.ChatData != "c2" || w.PublicData != "p" || w.HTML != "<b>hi</b>" {
		t.Fatalf("chat_data edit lost widget state: %+v", w)
	}
	wantStatus(t, edit(`,"public_data":""`), http.StatusOK)
	if w := latest(); w.PublicData != "" || w.ChatData != "c2" {
		t.Fatalf("empty public_data did not remove it: %+v", w)
	}

	for name, fields := range map[string]string{
		"not modified":       `,"chat_data":"c2"`,
		"nothing to change":  ``,
		"data too long":      `,"chat_data":"` + strings.Repeat("d", maxWidgetData+1) + `"`,
		"message not found":  `,"message_id":1`,
		"missing message_id": `,"message_id":0`,
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, edit(fields), http.StatusBadRequest)
		})
	}
	t.Run("message without widget", func(t *testing.T) {
		rec := widgetCall(srv, "sendMessage", `{"chat_id":42,"text":"plain"}`)
		var env struct {
			Result Message `json:"result"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		body := fmt.Sprintf(`{"chat_id":42,"message_id":%d,"chat_data":"x"}`, env.Result.MessageID)
		wantStatus(t, widgetCall(srv, "editMessageWidgetData", body), http.StatusBadRequest)
	})
}

func TestSharedWidgetPublicData(t *testing.T) {
	srv := New(Config{Quiet: true})
	first := sendTestWidget(t, srv, `,"is_shared":true,"public_data":"v1"`)
	id := first.Widget.ID
	wantStatus(t, widgetCall(srv, "sendSharedWidget", `{"chat_id":43,"widget_id":"`+id+`"}`), http.StatusOK)

	wantStatus(t, widgetCall(srv, "editSharedWidgetData", `{"widget_id":"`+id+`","public_data":"v2","cache_time":60}`), http.StatusOK)
	bot := srv.Store().Bot(widgetBot)
	for _, chatID := range []int64{42, 43} {
		if w := bot.Messages(chatID, 1)[0].Widget; w.PublicData != "v2" {
			t.Fatalf("chat %d public_data=%q, want v2", chatID, w.PublicData)
		}
	}

	// A second edit within a second is a flood.
	rec := widgetCall(srv, "editSharedWidgetData", `{"widget_id":"`+id+`","public_data":"v3"}`)
	wantStatus(t, rec, http.StatusTooManyRequests)
	if !strings.Contains(rec.Body.String(), `"retry_after":1`) {
		t.Fatalf("flood without retry_after: %s", rec.Body.String())
	}
	// editMessageWidgetData shares the limit for a shared widget's public data.
	body := fmt.Sprintf(`{"chat_id":42,"message_id":%d,"public_data":"v3"}`, first.MessageID)
	wantStatus(t, widgetCall(srv, "editMessageWidgetData", body), http.StatusTooManyRequests)

	bot.widgets.mu.Lock()
	bot.widgets.shared[id].lastEdit = time.Now().Add(-sharedWidgetEditInterval)
	bot.widgets.mu.Unlock()
	wantStatus(t, widgetCall(srv, "editMessageWidgetData", body), http.StatusOK)
	if w := bot.Messages(43, 1)[0].Widget; w.PublicData != "v3" {
		t.Fatalf("editMessageWidgetData did not reach other messages: %+v", w)
	}

	for name, body := range map[string]string{
		"unknown widget":      `{"widget_id":"nope","public_data":"x"}`,
		"missing public_data": `{"widget_id":"` + id + `"}`,
		"cache_time too low":  `{"widget_id":"` + id + `","public_data":"x","cache_time":4}`,
		"cache_time too high": `{"widget_id":"` + id + `","public_data":"x","cache_time":3601}`,
		"data too long":       `{"widget_id":"` + id + `","public_data":"` + strings.Repeat("d", maxWidgetData+1) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, widgetCall(srv, "editSharedWidgetData", body), http.StatusBadRequest)
		})
	}
}

func TestInjectWidgetQueries(t *testing.T) {
	srv := New(Config{Quiet: true})
	startPolling(t, srv, widgetBot)
	m := sendTestWidget(t, srv, `,"allow_queries":true,"is_shared":true`)
	closed := sendTestWidget(t, srv, `,"is_shared":true`)

	rec := injectWidgetQuery(srv, fmt.Sprintf(`"type":"widget_query","message_id":%d,"widget_data":"tap"`, m.MessageID))
	wantStatus(t, rec, http.StatusOK)
	rec = injectWidgetQuery(srv, `"type":"shared_widget_query","widget_id":"`+m.Widget.ID+`","widget_data":"tap2"`)
	wantStatus(t, rec, http.StatusOK)
	var response map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &response)

	updates := decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot"+widgetBot+"/getUpdates", ""))
	if len(updates) != 2 || updates[0].WidgetQuery == nil || updates[1].SharedWidgetQuery == nil {
		t.Fatalf("unexpected updates: %+v", updates)
	}
	q := updates[0].WidgetQuery
	if q.Data != "tap" || q.MessageID != m.MessageID || q.Chat == nil || q.Chat.ID != 42 ||
		q.WidgetID != m.Widget.ID || q.Type.Type != "data" || q.ChatInstance == "" {
		t.Fatalf("unexpected widget_query: %+v", q)
	}
	sq := updates[1].SharedWidgetQuery
	if sq.Data != "tap2" || sq.WidgetID != m.Widget.ID || response["widget_query_id"] != sq.ID {
		t.Fatalf("unexpected shared_widget_query %+v for response %v", sq, response)
	}

	for name, fields := range map[string]string{
		"empty data":        fmt.Sprintf(`"type":"widget_query","message_id":%d`, m.MessageID),
		"data too long":     fmt.Sprintf(`"type":"widget_query","message_id":%d,"widget_data":"%s"`, m.MessageID, strings.Repeat("d", maxWidgetQueryData+1)),
		"missing message":   `"type":"widget_query","widget_data":"x"`,
		"unknown message":   `"type":"widget_query","message_id":1,"widget_data":"x"`,
		"queries disabled":  fmt.Sprintf(`"type":"widget_query","message_id":%d,"widget_data":"x"`, closed.MessageID),
		"missing widget_id": `"type":"shared_widget_query","widget_data":"x"`,
		"unknown widget":    `"type":"shared_widget_query","widget_id":"nope","widget_data":"x"`,
		"shared disabled":   `"type":"shared_widget_query","widget_id":"` + closed.Widget.ID + `","widget_data":"x"`,
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, injectWidgetQuery(srv, fields), http.StatusBadRequest)
		})
	}
}

func TestWidgetQueryUserLimit(t *testing.T) {
	srv := New(Config{Quiet: true})
	startPolling(t, srv, widgetBot)
	m := sendTestWidget(t, srv, `,"allow_queries":true,"is_shared":true`)
	tap := fmt.Sprintf(`"type":"widget_query","message_id":%d,"widget_data":"tap"`, m.MessageID)
	shared := `"type":"shared_widget_query","widget_id":"` + m.Widget.ID + `","widget_data":"tap"`

	// The limit covers all of the user's widgets, shared ones included.
	for i := range widgetQueryBurst {
		fields := tap
		if i%2 == 1 {
			fields = shared
		}
		wantStatus(t, injectWidgetQuery(srv, fields), http.StatusOK)
	}
	wantStatus(t, injectWidgetQuery(srv, shared), http.StatusTooManyRequests)
	wantStatus(t, injectWidgetQuery(srv, tap), http.StatusTooManyRequests)
	if updates := decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot"+widgetBot+"/getUpdates", "")); len(updates) != widgetQueryBurst {
		t.Fatalf("delivered %d updates, want %d", len(updates), widgetQueryBurst)
	}

	// Another user has a separate budget.
	rec := pollingRequest(srv, http.MethodPost, "/debug/inject/update",
		`{"token":"`+widgetBot+`","chat_id":42,"user_id":43,`+shared+`}`)
	wantStatus(t, rec, http.StatusOK)

	// Once the window has passed the user can send again.
	bot := srv.Store().Bot(widgetBot)
	bot.widgets.mu.Lock()
	for i := range bot.widgets.sent[42] {
		bot.widgets.sent[42][i] = bot.widgets.sent[42][i].Add(-widgetQueryWindow)
	}
	bot.widgets.mu.Unlock()
	wantStatus(t, injectWidgetQuery(srv, tap), http.StatusOK)
}

func TestInjectWidgetQueryWebhook(t *testing.T) {
	received := make(chan []byte, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
	}))
	defer receiver.Close()

	srv := New(Config{Quiet: true, DefaultWebhookURL: receiver.URL})
	m := sendTestWidget(t, srv, `,"allow_queries":true`)
	wantStatus(t, injectWidgetQuery(srv, fmt.Sprintf(`"type":"widget_query","message_id":%d,"widget_data":"tap"`, m.MessageID)), http.StatusOK)
	var upd Update
	if err := json.Unmarshal(<-received, &upd); err != nil || upd.WidgetQuery == nil || upd.WidgetQuery.Data != "tap" {
		t.Fatalf("webhook did not receive widget_query: %+v err=%v", upd, err)
	}
}

func TestProxyForwardsWidgetAllowQueries(t *testing.T) {
	received := make(chan proxyForwardPayload, 2)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p proxyForwardPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		received <- p
	}))
	defer receiver.Close()

	srv := New(Config{Quiet: true})
	srv.store.Proxy.Register(widgetBot, receiver.URL, []int64{42})
	for _, allow := range []bool{true, false} {
		sendTestWidget(t, srv, fmt.Sprintf(`,"allow_queries":%t`, allow))
		select {
		case p := <-received:
			if p.Message.Widget == nil || p.AllowQueries != allow {
				t.Fatalf("allow_queries=%t: forwarded %+v", allow, p)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("proxy did not receive the widget")
		}
	}
}

func TestAnswerWidgetQuery(t *testing.T) {
	srv := New(Config{Quiet: true})
	startPolling(t, srv, widgetBot)
	m := sendTestWidget(t, srv, `,"allow_queries":true`)
	inject := func() string {
		rec := injectWidgetQuery(srv, fmt.Sprintf(`"type":"widget_query","message_id":%d,"widget_data":"tap"`, m.MessageID))
		var response struct {
			ID string `json:"widget_query_id"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.ID == "" {
			t.Fatalf("inject status=%d body=%s", rec.Code, rec.Body.String())
		}
		return response.ID
	}
	answer := func(id, data string) *httptest.ResponseRecorder {
		return widgetCall(srv, "answerWidgetQuery", `{"widget_query_id":"`+id+`","data":"`+data+`"}`)
	}

	id := inject()
	wantStatus(t, answer(id, strings.Repeat("d", maxWidgetData+1)), http.StatusBadRequest)
	wantStatus(t, answer(id, "ok"), http.StatusOK)
	wantStatus(t, answer(id, "again"), http.StatusBadRequest)
	wantStatus(t, answer("unknown", "x"), http.StatusBadRequest)
	wantStatus(t, widgetCall(srv, "answerWidgetQuery", `{}`), http.StatusBadRequest)

	expired := inject()
	bot := srv.Store().Bot(widgetBot)
	bot.widgets.mu.Lock()
	bot.widgets.queries[expired].issued = time.Now().Add(-widgetQueryTTL - time.Second)
	bot.widgets.mu.Unlock()
	rec := answer(expired, "late")
	wantStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "query ID is invalid") {
		t.Fatalf("unexpected expiry error: %s", rec.Body.String())
	}
}

func TestDebugChatRendersWidget(t *testing.T) {
	m := Message{Caption: "cap", Widget: &Widget{HTML: "<b>x</b>", Width: 300, Height: 200, PublicData: "<p>", ChatData: "c"}}
	out := string(renderMessageText(m, widgetBot, 42, "u"))
	for _, want := range []string{"cap", "html 8 B", "300×200", "public_data: <code>&lt;p&gt;</code>", "chat_data: <code>c</code>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render %q lacks %q", out, want)
		}
	}
	if mediaTag(m) != "widget" {
		t.Fatalf("media tag %q", mediaTag(m))
	}
}

func TestDebugChatWidgetPage(t *testing.T) {
	srv := New(Config{Quiet: true})
	m := sendTestWidget(t, srv, `,"allow_queries":true,"chat_data":"</script><b>"`)
	page := fmt.Sprintf("/debug/chat/%s/42/widget/%d?name=ann", widgetBot, m.MessageID)

	rec := pollingRequest(srv, http.MethodGet, page, "")
	wantStatus(t, rec, http.StatusOK)
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.HasPrefix(csp, "sandbox allow-scripts;") {
		t.Fatalf("widget page is not sandboxed: %q", csp)
	}
	body := rec.Body.String()
	for _, want := range []string{"window.TelegramWidget", `"canQuery":true`, `"chatData":"`, `"first_name":"ann"`, "<b>hi</b>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("widget page lacks %q", want)
		}
	}
	// chat_data must not close the runtime's script element.
	if strings.Contains(body, `"chatData":"</script>`) {
		t.Fatal("chat_data is not escaped in the runtime init")
	}
	if strings.Contains(body, "/*INIT*/") {
		t.Fatal("runtime init placeholder not replaced")
	}
	wantStatus(t, pollingRequest(srv, http.MethodGet, fmt.Sprintf("/debug/chat/%s/42/widget/1", widgetBot), ""), http.StatusNotFound)

	rec = pollingRequest(srv, http.MethodGet, fmt.Sprintf("/debug/chat/%s/42/messages?name=ann", widgetBot), "")
	for _, want := range []string{`<iframe class="widget"`, `sandbox="allow-scripts"`, `src="` + strings.ReplaceAll(page, "&", "&amp;") + `"`, `data-chat-data="&lt;/script&gt;&lt;b&gt;"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("messages lack %q: %s", want, rec.Body.String())
		}
	}
}

func TestDebugChatWidgetQuery(t *testing.T) {
	srv := New(Config{Quiet: true})
	startPolling(t, srv, widgetBot)
	open := sendTestWidget(t, srv, `,"allow_queries":true`)
	closed := sendTestWidget(t, srv, ``)
	query := func(messageID int, data string) map[string]any {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/debug/chat/%s/42/widget/%d/query", widgetBot, messageID),
			strings.NewReader(url.Values{"data": {data}, "name": {"ann"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		srv.Handler().ServeHTTP(rec, req)
		var out map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("query status=%d body=%s", rec.Code, rec.Body.String())
		}
		return out
	}
	// The bot answers each query with "re:<data>", or without data for "empty".
	go func() {
		for i := 0; i < 2; i++ {
			rec := pollingRequest(srv, http.MethodGet, "/bot"+widgetBot+"/getUpdates?timeout=5", "")
			var env struct {
				Result []Update `json:"result"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &env)
			for _, u := range env.Result {
				q := u.WidgetQuery
				answer := `{"widget_query_id":"` + q.ID + `","data":"re:` + q.Data + `"}`
				if q.Data == "empty" {
					answer = `{"widget_query_id":"` + q.ID + `"}`
				}
				widgetCall(srv, "answerWidgetQuery", answer)
				pollingRequest(srv, http.MethodGet, fmt.Sprintf("/bot%s/getUpdates?offset=%d", widgetBot, u.UpdateID+1), "")
			}
		}
	}()

	if got := query(open.MessageID, "ping"); got["result"] != "re:ping" || got["code"] != nil {
		t.Fatalf("answer not passed to the widget: %v", got)
	}
	if got := query(open.MessageID, "empty"); got["result"] != nil || got["code"] != nil {
		t.Fatalf("answer without data must resolve with null: %v", got)
	}
	if got := query(closed.MessageID, "x"); got["code"] != "NOT_ALLOWED" {
		t.Fatalf("widget without allow_queries: %v", got)
	}

	// Exhaust the per-user limit through the debug inject endpoint.
	bot := srv.Store().Bot(widgetBot)
	bot.widgets.mu.Lock()
	now := time.Now()
	bot.widgets.sent[42] = []time.Time{now, now, now, now, now}
	bot.widgets.mu.Unlock()
	if got := query(open.MessageID, "x"); got["code"] != "RATE_LIMITED" {
		t.Fatalf("flood must map to RATE_LIMITED: %v", got)
	}
}

func TestWidgetSurvivesCaptionAndMarkupEdits(t *testing.T) {
	srv := New(Config{Quiet: true})
	m := sendTestWidget(t, srv, `,"caption":"v1","chat_data":"c1"`)
	edit := func(method, fields string) {
		t.Helper()
		wantStatus(t, widgetCall(srv, method, fmt.Sprintf(`{"chat_id":42,"message_id":%d%s}`, m.MessageID, fields)), http.StatusOK)
	}
	latest := func() Message { return srv.Store().Bot(widgetBot).Messages(42, 1)[0] }

	edit("editMessageReplyMarkup", `,"reply_markup":{"inline_keyboard":[[{"text":"go","callback_data":"go"}]]}`)
	if got := latest(); got.Widget == nil || got.Caption != "v1" || got.ReplyMarkup == nil {
		t.Fatalf("markup edit dropped the widget or caption: %+v", got)
	}
	edit("editMessageCaption", `,"caption":"v2","reply_markup":{"inline_keyboard":[[{"text":"go","callback_data":"go"}]]}`)
	if got := latest(); got.Widget == nil || got.Caption != "v2" {
		t.Fatalf("caption edit dropped the widget: %+v", got)
	}
	edit("editMessageWidgetData", `,"chat_data":"c2"`)
	got := latest()
	if got.Widget.ChatData != "c2" || got.Caption != "v2" || got.ReplyMarkup == nil || len(got.ReplyMarkup.InlineKeyboard) != 1 {
		t.Fatalf("widget data edit undid earlier edits: %+v", got)
	}

	rec := pollingRequest(srv, http.MethodGet, fmt.Sprintf("/debug/chat/%s/42/messages?name=ann", widgetBot), "")
	if !strings.Contains(rec.Body.String(), `<iframe class="widget"`) {
		t.Fatal("debug chat lost the widget after edits")
	}
}

func TestInjectWidgetQueryPrivateChat(t *testing.T) {
	srv := New(Config{Quiet: true})
	startPolling(t, srv, widgetBot)
	m := sendTestWidget(t, srv, `,"allow_queries":true`)
	rec := pollingRequest(srv, http.MethodPost, "/debug/inject/update", fmt.Sprintf(
		`{"token":%q,"chat_id":42,"first_name":"Ann","username":"ann","type":"widget_query","message_id":%d,"widget_data":"x"}`, widgetBot, m.MessageID))
	wantStatus(t, rec, http.StatusOK)
	updates := decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot"+widgetBot+"/getUpdates", ""))
	if chat := updates[0].WidgetQuery.Chat; chat.FirstName != "Ann" || chat.Username != "ann" {
		t.Fatalf("private chat lacks the user's names: %+v", chat)
	}
}

func TestCheckWidgetHTMLSizes(t *testing.T) {
	if err := checkWidgetHTML(strings.Repeat("x", maxWidgetHTMLCompressed-11)); err != nil {
		t.Fatalf("short HTML refused: %v", err)
	}
	random := make([]byte, 6000)
	_, _ = rand.Read(random)
	if err := checkWidgetHTML(base64.StdEncoding.EncodeToString(random)); err == nil {
		t.Fatal("incompressible HTML over 4096 bytes accepted")
	}
}
