package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"github.com/kolomiichenko/telegym/pkg/mock"
)

const (
	testMockToken  = "1234567890:telegym_default_mock_token_xxxxxxxx"
	testProxyToken = "1111111111:proxy_test_token_xxxxxxxxxxxxxxxxxx"
	testChatID     = 42
	testRealMsgID  = 500
)

// apiCall is one Bot API call the fake Telegram received.
type apiCall struct {
	method string
	params map[string]any
}

// fakeTelegram answers the proxy bot's Bot API calls and reports each one.
func fakeTelegram(t *testing.T) (*httptest.Server, <-chan apiCall) {
	t.Helper()
	calls := make(chan apiCall, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var params map[string]any
		_ = json.NewDecoder(r.Body).Decode(&params)
		calls <- apiCall{method: method, params: params}

		result := fmt.Sprintf(`{"message_id":%d,"date":1,"chat":{"id":%d,"type":"private"},"widget":{"html":"x"}}`,
			testRealMsgID, testChatID)
		if method == "answerWidgetQuery" || method == "editMessageWidgetData" {
			result = `true`
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":`+result+`}`)
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

func waitCall(t *testing.T, calls <-chan apiCall, method string) apiCall {
	t.Helper()
	for {
		select {
		case c := <-calls:
			if c.method == method {
				return c
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("real Telegram did not receive %s", method)
		}
	}
}

// botCall calls the mock's Bot API as the bot-under-test.
func botCall(t *testing.T, mockURL, method, body string) json.RawMessage {
	t.Helper()
	resp, err := http.Post(mockURL+"/bot"+testMockToken+"/"+method, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil || !env.OK {
		t.Fatalf("%s: ok=%t err=%v", method, env.OK, err)
	}
	return env.Result
}

// testStand runs the proxy between a real mock and a fake Telegram, with
// chat testChatID bound to the proxy and the bot-under-test long-polling.
func testStand(t *testing.T) (*proxy, string, <-chan apiCall) {
	t.Helper()
	mockSrv := httptest.NewServer(mock.New(mock.Config{Quiet: true}).Handler())
	t.Cleanup(mockSrv.Close)
	tg, calls := fakeTelegram(t)

	bot, err := telego.NewBot(testProxyToken, telego.WithAPIServer(tg.URL), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{
		cfg:        config{mockURL: mockSrv.URL, mockBotToken: testMockToken},
		bot:        bot,
		httpc:      &http.Client{Timeout: 5 * time.Second},
		messageMap: map[int]int{},
		realIDs:    map[int]int{},
		widgetIDs:  map[string]string{},
		realWidget: map[string]string{},
	}
	proxySrv := httptest.NewServer(http.HandlerFunc(p.handleForward))
	t.Cleanup(proxySrv.Close)
	p.cfg.publicURL = proxySrv.URL
	p.bindIfNeeded(testChatID)
	botCall(t, mockSrv.URL, "deleteWebhook", `{}`) // the bot-under-test long-polls
	return p, mockSrv.URL, calls
}

// waitRelayed waits until the proxy maps the relayed message, which happens
// once real Telegram responds and before a user can see it.
func waitRelayed(t *testing.T, p *proxy) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); p.lookupMockID(testRealMsgID) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("proxy did not map the relayed message")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWidgetRoundTrip(t *testing.T) {
	ctx := context.Background()
	p, mockURL, calls := testStand(t)

	// The bot-under-test sends a widget that may query it.
	var sent struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(botCall(t, mockURL, "sendWidget",
		fmt.Sprintf(`{"chat_id":%d,"html":"<b>hi</b>","allow_queries":true}`, testChatID)), &sent)
	if c := waitCall(t, calls, "sendWidget"); c.params["html"] != "<b>hi</b>" || c.params["allow_queries"] != true {
		t.Fatalf("sendWidget params: %v", c.params)
	}
	waitRelayed(t, p)

	// The real widget queries the bot; the bot-under-test answers on the mock.
	p.handleUpdate(ctx, telego.Update{WidgetQuery: &telego.WidgetQuery{
		ID:        "real-query",
		From:      telego.User{ID: testChatID, FirstName: "dev"},
		Chat:      &telego.Chat{ID: testChatID, Type: telego.ChatTypePrivate},
		MessageID: testRealMsgID,
		Type:      &telego.WidgetQueryTypeData{Type: telego.WidgetQueryData},
		Data:      "tap",
	}})
	var updates []struct {
		WidgetQuery *struct {
			ID        string `json:"id"`
			MessageID int    `json:"message_id"`
			Data      string `json:"data"`
		} `json:"widget_query"`
	}
	_ = json.Unmarshal(botCall(t, mockURL, "getUpdates", `{}`), &updates)
	if len(updates) != 1 || updates[0].WidgetQuery == nil ||
		updates[0].WidgetQuery.MessageID != sent.MessageID || updates[0].WidgetQuery.Data != "tap" {
		t.Fatalf("bot-under-test got updates %+v", updates)
	}
	botCall(t, mockURL, "answerWidgetQuery",
		`{"widget_query_id":"`+updates[0].WidgetQuery.ID+`","data":"pong"}`)
	if c := waitCall(t, calls, "answerWidgetQuery"); c.params["widget_query_id"] != "real-query" || c.params["data"] != "pong" {
		t.Fatalf("answerWidgetQuery params: %v", c.params)
	}

	// A widget data edit lands on the relayed message instead of a new one.
	botCall(t, mockURL, "editMessageWidgetData",
		fmt.Sprintf(`{"chat_id":%d,"message_id":%d,"chat_data":"v2"}`, testChatID, sent.MessageID))
	c := waitCall(t, calls, "editMessageWidgetData")
	if c.params["message_id"] != float64(testRealMsgID) || c.params["chat_data"] != "v2" {
		t.Fatalf("editMessageWidgetData params: %v", c.params)
	}
}

func TestEditsApplyToRelayedMessage(t *testing.T) {
	p, mockURL, calls := testStand(t)

	var sent struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(botCall(t, mockURL, "sendMessage",
		fmt.Sprintf(`{"chat_id":%d,"text":"v1"}`, testChatID)), &sent)
	waitCall(t, calls, "sendMessage")
	waitRelayed(t, p)

	keyboard := `{"inline_keyboard":[[{"text":"ok","callback_data":"ok"}]]}`
	tests := []struct {
		method string
		body   string
		want   map[string]any
	}{
		{"editMessageText", `"text":"v2","reply_markup":` + keyboard, map[string]any{"text": "v2"}},
		{"editMessageReplyMarkup", `"reply_markup":` + keyboard, map[string]any{}},
		{"editMessageCaption", `"caption":"c2"`, map[string]any{"caption": "c2"}},
	}
	for _, tt := range tests {
		botCall(t, mockURL, tt.method, fmt.Sprintf(`{"chat_id":%d,"message_id":%d,%s}`, testChatID, sent.MessageID, tt.body))
		c := waitCall(t, calls, tt.method)
		if c.params["message_id"] != float64(testRealMsgID) {
			t.Fatalf("%s edited message %v, want %d", tt.method, c.params["message_id"], testRealMsgID)
		}
		for k, v := range tt.want {
			if c.params[k] != v {
				t.Fatalf("%s %s=%v, want %v", tt.method, k, c.params[k], v)
			}
		}
		if strings.Contains(tt.body, "reply_markup") && c.params["reply_markup"] == nil {
			t.Fatalf("%s dropped reply_markup", tt.method)
		}
	}

	// An edit of a message the proxy never relayed is not sent as a new one.
	botCall(t, mockURL, "editMessageText", fmt.Sprintf(`{"chat_id":%d,"message_id":%d,"text":"x"}`, testChatID, sent.MessageID+100))
	// Forwards are asynchronous, so watch a window instead of the next call.
	select {
	case c := <-calls:
		t.Fatalf("unexpected call %s %v", c.method, c.params)
	case <-time.After(300 * time.Millisecond):
	}
}
