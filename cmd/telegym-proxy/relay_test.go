package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"
)

// expectNoCall fails if real Telegram receives any call within a short window.
func expectNoCall(t *testing.T, calls <-chan apiCall) {
	t.Helper()
	select {
	case c := <-calls:
		t.Fatalf("unexpected call %s %v", c.method, c.params)
	case <-time.After(300 * time.Millisecond):
	}
}

// pollUpdates drains the bot-under-test's pending updates on the mock.
func pollUpdates(t *testing.T, mockURL string) []map[string]any {
	t.Helper()
	var updates []map[string]any
	if err := json.Unmarshal(botCall(t, mockURL, "getUpdates", `{"timeout":0}`), &updates); err != nil {
		t.Fatal(err)
	}
	if len(updates) > 0 {
		last := updates[len(updates)-1]["update_id"].(float64)
		botCall(t, mockURL, "getUpdates", fmt.Sprintf(`{"offset":%d,"timeout":0}`, int(last)+1))
	}
	return updates
}

func TestBuildMarkup(t *testing.T) {
	inline := [][]telego.InlineKeyboardButton{{{Text: "a", CallbackData: "a"}}}
	tests := []struct {
		name string
		in   *incomingMarkup
		want telego.ReplyMarkup
	}{
		{"nil", nil, nil},
		{"empty", &incomingMarkup{}, nil},
		{"remove wins", &incomingMarkup{RemoveKeyboard: true, ForceReply: true, Selective: true},
			&telego.ReplyKeyboardRemove{RemoveKeyboard: true, Selective: true}},
		{"force reply", &incomingMarkup{ForceReply: true, InputFieldPlaceholder: "type"},
			&telego.ForceReply{ForceReply: true, InputFieldPlaceholder: "type"}},
		{"reply keyboard", &incomingMarkup{
			Keyboard:        [][]incomingKbdButton{{{Text: "phone", RequestContact: true}, {Text: "geo", RequestLocation: true}}},
			ResizeKeyboard:  true,
			OneTimeKeyboard: true,
			IsPersistent:    true,
			InlineKeyboard:  inline,
		}, &telego.ReplyKeyboardMarkup{
			Keyboard:        [][]telego.KeyboardButton{{{Text: "phone", RequestContact: true}, {Text: "geo", RequestLocation: true}}},
			ResizeKeyboard:  true,
			OneTimeKeyboard: true,
			IsPersistent:    true,
		}},
		{"inline keyboard", &incomingMarkup{InlineKeyboard: inline}, &telego.InlineKeyboardMarkup{InlineKeyboard: inline}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildMarkup(tt.in)
			if tt.want == nil {
				// A typed nil would serialize as {"inline_keyboard":null}.
				if got != nil {
					t.Fatalf("got %#v, want untyped nil", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestInboundMessageAndCallback(t *testing.T) {
	ctx := context.Background()
	p, mockURL, calls := testStand(t)
	user := telego.User{ID: testChatID, FirstName: "dev", Username: "dev"}
	chat := telego.Chat{ID: testChatID, Type: telego.ChatTypePrivate}

	p.handleUpdate(ctx, telego.Update{Message: &telego.Message{From: &user, Chat: chat, Text: "hello"}})
	updates := pollUpdates(t, mockURL)
	if len(updates) != 1 {
		t.Fatalf("bot-under-test got %d updates, want 1: %v", len(updates), updates)
	}
	if msg, _ := updates[0]["message"].(map[string]any); msg == nil || msg["text"] != "hello" {
		t.Fatalf("message update: %v", updates[0])
	}

	// A button press on a relayed message reaches the bot with the mock's id.
	var sent struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(botCall(t, mockURL, "sendMessage", fmt.Sprintf(
		`{"chat_id":%d,"text":"pick","reply_markup":{"inline_keyboard":[[{"text":"ok","callback_data":"ok"}]]}}`,
		testChatID)), &sent)
	if c := waitCall(t, calls, "sendMessage"); c.params["reply_markup"] == nil {
		t.Fatalf("sendMessage dropped reply_markup: %v", c.params)
	}
	waitRelayed(t, p)

	p.handleUpdate(ctx, telego.Update{CallbackQuery: &telego.CallbackQuery{
		ID:      "real-cb",
		From:    user,
		Message: &telego.Message{MessageID: testRealMsgID, Chat: chat},
		Data:    "ok",
	}})
	if c := waitCall(t, calls, "answerCallbackQuery"); c.params["callback_query_id"] != "real-cb" {
		t.Fatalf("answerCallbackQuery params: %v", c.params)
	}
	updates = pollUpdates(t, mockURL)
	if len(updates) != 1 {
		t.Fatalf("bot-under-test got %d updates, want 1: %v", len(updates), updates)
	}
	cq, _ := updates[0]["callback_query"].(map[string]any)
	if cq == nil || cq["data"] != "ok" {
		t.Fatalf("callback update: %v", updates[0])
	}
	if msg, _ := cq["message"].(map[string]any); msg == nil || msg["message_id"] != float64(sent.MessageID) {
		t.Fatalf("callback message = %v, want mock id %d", cq["message"], sent.MessageID)
	}
}

func TestDisallowedUserIsIgnored(t *testing.T) {
	ctx := context.Background()
	p, mockURL, calls := testStand(t)
	p.cfg.allowedUserID = testChatID + 1
	stranger := telego.User{ID: testChatID}
	chat := telego.Chat{ID: testChatID, Type: telego.ChatTypePrivate}

	p.handleUpdate(ctx, telego.Update{Message: &telego.Message{From: &stranger, Chat: chat, Text: "hi"}})
	p.handleUpdate(ctx, telego.Update{Message: &telego.Message{Chat: chat, Text: "no sender"}})
	p.handleUpdate(ctx, telego.Update{CallbackQuery: &telego.CallbackQuery{ID: "cb", From: stranger}})
	p.handleUpdate(ctx, telego.Update{WidgetQuery: &telego.WidgetQuery{ID: "wq", From: stranger}})
	p.handleUpdate(ctx, telego.Update{SharedWidgetQuery: &telego.SharedWidgetQuery{ID: "sq", From: stranger}})

	if updates := pollUpdates(t, mockURL); len(updates) != 0 {
		t.Fatalf("bot-under-test got updates from a disallowed user: %v", updates)
	}
	expectNoCall(t, calls)
}

func TestWidgetQueriesForUnknownTargetsAreDropped(t *testing.T) {
	ctx := context.Background()
	p, mockURL, calls := testStand(t)
	user := telego.User{ID: testChatID}
	data := &telego.WidgetQueryTypeData{Type: telego.WidgetQueryData}

	p.handleUpdate(ctx, telego.Update{WidgetQuery: &telego.WidgetQuery{ID: "wq", From: user, MessageID: 999, Type: data}})
	p.handleUpdate(ctx, telego.Update{SharedWidgetQuery: &telego.SharedWidgetQuery{ID: "sq", From: user, WidgetID: "nope", Type: data}})
	// Non-data queries need a TON wallet the mock can't emulate.
	p.relayWidgetQuery(ctx, "wq", nil, user, testChatID, map[string]any{"type": "widget_query"})

	if updates := pollUpdates(t, mockURL); len(updates) != 0 {
		t.Fatalf("bot-under-test got updates: %v", updates)
	}
	expectNoCall(t, calls)
}

func TestSharedWidgetRoundTrip(t *testing.T) {
	ctx := context.Background()
	p, mockURL, calls := testStand(t)

	var sent struct {
		Widget struct {
			ID string `json:"id"`
		} `json:"widget"`
	}
	_ = json.Unmarshal(botCall(t, mockURL, "sendWidget",
		fmt.Sprintf(`{"chat_id":%d,"html":"<i>w</i>","is_shared":true,"allow_queries":true}`, testChatID)), &sent)
	if sent.Widget.ID == "" {
		t.Fatal("mock returned no shared widget id")
	}
	if c := waitCall(t, calls, "sendWidget"); c.params["is_shared"] != true {
		t.Fatalf("sendWidget params: %v", c.params)
	}
	for deadline := time.Now().Add(5 * time.Second); p.lookupRealWidgetID(sent.Widget.ID) == ""; {
		if time.Now().After(deadline) {
			t.Fatal("proxy did not map the shared widget")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := p.lookupMockWidgetID(testRealWidgetID); got != sent.Widget.ID {
		t.Fatalf("lookupMockWidgetID = %q, want %q", got, sent.Widget.ID)
	}

	// Resending the shared widget reuses the proxy bot's own copy.
	botCall(t, mockURL, "sendSharedWidget",
		fmt.Sprintf(`{"chat_id":%d,"widget_id":%q,"chat_data":"cd"}`, testChatID, sent.Widget.ID))
	if c := waitCall(t, calls, "sendSharedWidget"); c.params["widget_id"] != testRealWidgetID || c.params["chat_data"] != "cd" {
		t.Fatalf("sendSharedWidget params: %v", c.params)
	}

	// A shared widget query reaches the bot with the mock's widget id.
	p.handleUpdate(ctx, telego.Update{SharedWidgetQuery: &telego.SharedWidgetQuery{
		ID:       "real-sq",
		From:     telego.User{ID: testChatID},
		WidgetID: testRealWidgetID,
		Type:     &telego.WidgetQueryTypeData{Type: telego.WidgetQueryData},
		Data:     "tap",
	}})
	updates := pollUpdates(t, mockURL)
	if len(updates) != 1 {
		t.Fatalf("bot-under-test got %d updates, want 1: %v", len(updates), updates)
	}
	sq, _ := updates[0]["shared_widget_query"].(map[string]any)
	if sq == nil || sq["widget_id"] != sent.Widget.ID || sq["data"] != "tap" {
		t.Fatalf("shared_widget_query update: %v", updates[0])
	}
}

func TestRelayUploadedPhoto(t *testing.T) {
	p, mockURL, calls := testStand(t)

	var img bytes.Buffer
	_ = png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("chat_id", fmt.Sprint(testChatID))
	_ = w.WriteField("caption", "look")
	part, _ := w.CreateFormFile("photo", "pic.png")
	_, _ = part.Write(img.Bytes())
	_ = w.Close()
	resp, err := http.Post(mockURL+"/bot"+testMockToken+"/sendPhoto", w.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sendPhoto on mock: HTTP %d", resp.StatusCode)
	}

	c := waitCall(t, calls, "sendPhoto")
	if c.params["photo"] != "pic.png" || c.params["caption"] != "look" {
		t.Fatalf("sendPhoto params: %v", c.params)
	}
	waitRelayed(t, p)
}

func TestForwardToRealFallbacks(t *testing.T) {
	ctx := context.Background()
	p, _, calls := testStand(t)
	chat := telego.Chat{ID: testChatID, Type: telego.ChatTypePrivate}

	tests := []struct {
		name     string
		msg      telego.Message
		method   string
		wantText string
	}{
		{"missing photo", telego.Message{Chat: chat, Caption: "cap", Photo: []telego.PhotoSize{{FileID: "gone"}}},
			"sendMessage", "cap\n\n[photo unavailable: gone]"},
		{"missing video", telego.Message{Chat: chat, Video: &telego.Video{FileID: "gone"}},
			"sendMessage", "[video unavailable: gone]"},
		{"missing animation", telego.Message{Chat: chat, Animation: &telego.Animation{FileID: "gone"}},
			"sendMessage", "[animation unavailable: gone]"},
		{"sticker", telego.Message{Chat: chat, Sticker: &telego.Sticker{FileID: "s"}}, "sendMessage", "[sticker]"},
		{"sticker with caption", telego.Message{Chat: chat, Caption: "hi", Sticker: &telego.Sticker{FileID: "s"}},
			"sendMessage", "[sticker: hi]"},
		{"caption only", telego.Message{Chat: chat, Caption: "just caption"}, "sendMessage", "just caption"},
		{"dice", telego.Message{Chat: chat, Dice: &telego.Dice{Emoji: "🎯"}}, "sendDice", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := p.forwardToReal(ctx, tt.msg, false, nil); err != nil {
				t.Fatal(err)
			}
			c := waitCall(t, calls, tt.method)
			if tt.wantText != "" && c.params["text"] != tt.wantText {
				t.Fatalf("text = %q, want %q", c.params["text"], tt.wantText)
			}
			if tt.method == "sendDice" && c.params["emoji"] != "🎯" {
				t.Fatalf("emoji = %v", c.params["emoji"])
			}
		})
	}

	// Nothing to show: no call at all.
	if err := p.forwardToReal(ctx, telego.Message{Chat: chat}, false, nil); err != nil {
		t.Fatal(err)
	}
	expectNoCall(t, calls)
}

func TestEditRealSkipsUnrelayableEdits(t *testing.T) {
	ctx := context.Background()
	p, _, calls := testStand(t)
	p.remember(7, testRealMsgID)
	chat := telego.Chat{ID: testChatID}

	if err := p.editReal(ctx, "editMessageMedia", telego.Message{MessageID: 7, Chat: chat}, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.editReal(ctx, "editMessageWidgetData", telego.Message{MessageID: 7, Chat: chat}, nil); err != nil {
		t.Fatal(err)
	}
	expectNoCall(t, calls)
}

func TestHandleForward(t *testing.T) {
	p, _, calls := testStand(t)

	rec := httptest.NewRecorder()
	p.handleForward(rec, httptest.NewRequest(http.MethodPost, "/forward", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON: status %d, want 400", rec.Code)
	}

	// A reply keyboard survives even though telego.Message can't hold one.
	rec = httptest.NewRecorder()
	p.handleForward(rec, httptest.NewRequest(http.MethodPost, "/forward", strings.NewReader(fmt.Sprintf(
		`{"message":{"message_id":3,"chat":{"id":%d,"type":"private"},"text":"t","reply_markup":{"keyboard":[[{"text":"k"}]],"resize_keyboard":true}}}`,
		testChatID))))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204", rec.Code)
	}
	c := waitCall(t, calls, "sendMessage")
	markup, _ := c.params["reply_markup"].(map[string]any)
	if markup == nil || markup["keyboard"] == nil || markup["resize_keyboard"] != true {
		t.Fatalf("reply_markup = %v", c.params["reply_markup"])
	}
	if p.lookupRealID(3) != testRealMsgID {
		t.Fatalf("relayed message not mapped")
	}
}

func TestHandleForwardReportsTelegramErrors(t *testing.T) {
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	t.Cleanup(tg.Close)
	bot, err := telego.NewBot(testProxyToken, telego.WithAPIServer(tg.URL), telego.WithDiscardLogger())
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{bot: bot, messageMap: map[int]int{}, realIDs: map[int]int{}}

	rec := httptest.NewRecorder()
	p.handleForward(rec, httptest.NewRequest(http.MethodPost, "/forward",
		strings.NewReader(`{"message":{"message_id":1,"chat":{"id":1},"text":"t"}}`)))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
	if p.lookupRealID(1) != 0 {
		t.Fatal("failed send must not be mapped")
	}

	// Real Telegram rejects no-op edits; the mock doesn't, so they're not errors.
	tg.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`))
	})
	p.remember(1, 10)
	if err := p.editReal(context.Background(), "editMessageText", telego.Message{MessageID: 1, Text: "same"}, nil); err != nil {
		t.Fatalf("no-op edit: %v", err)
	}
}

func TestAwaitWidgetAnswerTimesOut(t *testing.T) {
	p, _, _ := testStand(t)
	if _, ok := p.awaitWidgetAnswer(context.Background(), "nope", time.Now().Add(150*time.Millisecond)); ok {
		t.Fatal("found an answer that was never sent")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := p.awaitWidgetAnswer(ctx, "nope", time.Now().Add(time.Minute)); ok {
		t.Fatal("found an answer after cancel")
	}
}

func TestHelpers(t *testing.T) {
	if got := shortToken("short"); got != "short" {
		t.Errorf("shortToken(short) = %q", got)
	}
	if got := shortToken(testMockToken); got != "123456…" {
		t.Errorf("shortToken(long) = %q", got)
	}
	for _, tt := range []struct {
		s, sub string
		want   int
	}{
		{`inline; filename="a.png"`, `filename="`, 8},
		{"abc", "d", -1},
		{"ab", "abc", -1},
		{"abc", "", 0},
	} {
		if got := indexOf(tt.s, tt.sub); got != tt.want {
			t.Errorf("indexOf(%q, %q) = %d, want %d", tt.s, tt.sub, got, tt.want)
		}
	}

	t.Setenv("TELEGYM_TEST_ENV", "set")
	if envOr("TELEGYM_TEST_ENV", "def") != "set" || envOr("TELEGYM_TEST_UNSET", "def") != "def" {
		t.Error("envOr")
	}

	p := &proxy{realWidget: map[string]string{}, widgetIDs: map[string]string{}}
	p.rememberWidget("mock", "")
	if p.lookupRealWidgetID("mock") != "" || p.lookupRealWidgetID("") != "" {
		t.Error("empty real widget id must not be remembered")
	}
}
