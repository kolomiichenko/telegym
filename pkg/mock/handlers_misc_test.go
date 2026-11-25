package mock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// debugPOST sends a JSON body to a debug endpoint.
func debugPOST(srv *Server, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestGenericDispatch(t *testing.T) {
	srv := New(Config{Quiet: true})

	// A Message-returning method gets plausible chat, id and sender.
	code, resp := callBot(t, srv, "sendLocation", `{"chat_id":77,"latitude":1,"longitude":2}`)
	if code != http.StatusOK || !resp.OK {
		t.Fatalf("sendLocation: %d %+v", code, resp)
	}
	m, _ := resp.Result.(map[string]any)
	chat, _ := m["chat"].(map[string]any)
	from, _ := m["from"].(map[string]any)
	if chat["id"] != float64(77) || m["message_id"] == float64(0) || m["date"] == nil || from["is_bot"] != true {
		t.Fatalf("sendLocation result: %v", m)
	}

	// Without chat_id the message id comes from the global counter.
	_, resp = callBot(t, srv, "sendLocation", `{}`)
	if m, _ := resp.Result.(map[string]any); m["message_id"] == float64(0) {
		t.Fatalf("sendLocation without chat_id: %v", m)
	}

	// A struct-returning method gets an object, a boolean method true.
	if _, resp = callBot(t, srv, "getChat", `{"chat_id":77}`); !isObject(resp.Result) {
		t.Fatalf("getChat result: %#v", resp.Result)
	}
	if _, resp = callBot(t, srv, "pinChatMessage", `{}`); resp.Result != true {
		t.Fatalf("pinChatMessage result: %#v", resp.Result)
	}
	// Unknown methods still answer ok:true.
	if code, resp = callBot(t, srv, "noSuchMethod", `{}`); code != http.StatusOK || resp.Result != true {
		t.Fatalf("unknown method: %d %+v", code, resp)
	}
}

func isObject(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

func TestEditMessageMedia(t *testing.T) {
	srv := New(Config{Quiet: true})
	_, resp := callBot(t, srv, "editMessageMedia", `{"chat_id":5,"message_id":9,"caption":"new"}`)
	var m Message
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.MessageID != 9 || m.Chat.ID != 5 || m.Caption != "new" || len(m.Photo) != 1 || m.Photo[0].FileID == "" {
		t.Fatalf("editMessageMedia result: %+v", m)
	}
	if msgs := srv.Store().Bot("903:faults").Messages(5, 10); len(msgs) != 1 || msgs[0].Caption != "new" {
		t.Fatalf("stored messages: %+v", msgs)
	}

	if code, _ := callBot(t, srv, "editMessageMedia", `{`); code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", code)
	}
}

func TestAnswerCallbackQueryToast(t *testing.T) {
	srv := New(Config{Quiet: true})
	callBot(t, srv, "deleteWebhook", `{}`) // queue updates for polling
	rec := debugPOST(srv, "/debug/inject/update", `{"token":"903:faults","chat_id":11,"user_id":11,"message_id":1,"callback_data":"x"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("inject: %d %s", rec.Code, rec.Body)
	}
	_, resp := callBot(t, srv, "getUpdates", `{}`)
	raw, _ := json.Marshal(resp.Result)
	var updates []Update
	if err := json.Unmarshal(raw, &updates); err != nil || len(updates) != 1 || updates[0].CallbackQuery == nil {
		t.Fatalf("updates: %s (%v)", raw, err)
	}
	cbID := updates[0].CallbackQuery.ID

	bot := srv.Store().Bot("903:faults")
	// No text means no toast.
	callBot(t, srv, "answerCallbackQuery", `{"callback_query_id":"`+cbID+`"}`)
	if _, ok := bot.PopToast(11); ok {
		t.Fatal("toast pushed for an answer without text")
	}
	code, resp := callBot(t, srv, "answerCallbackQuery", `{"callback_query_id":"`+cbID+`","text":"done","show_alert":true}`)
	if code != http.StatusOK || resp.Result != true {
		t.Fatalf("answerCallbackQuery: %d %+v", code, resp)
	}
	if toast, ok := bot.PopToast(11); !ok || toast.Text != "done" || !toast.ShowAlert {
		t.Fatalf("toast = %+v, %t", toast, ok)
	}
	// An unknown callback id is answered but surfaces nowhere.
	callBot(t, srv, "answerCallbackQuery", `{"callback_query_id":"nope","text":"lost"}`)
	if _, ok := bot.PopToast(11); ok {
		t.Fatal("toast pushed for an unknown callback")
	}
}

func TestRegisterCallbackBounded(t *testing.T) {
	bot := NewStore().Bot("953:cb")
	bot.RegisterCallback("", 1)
	bot.RegisterCallback("zero", 0)
	if bot.LookupCallbackChat("") != 0 || bot.LookupCallbackChat("zero") != 0 {
		t.Fatal("empty id or chat was registered")
	}
	for i := range 5000 {
		bot.RegisterCallback("cb"+strconv.Itoa(i), int64(i+1))
	}
	bot.RegisterCallback("last", 42)
	if got := bot.LookupCallbackChat("last"); got != 42 {
		t.Fatalf("LookupCallbackChat(last) = %d, want 42", got)
	}
	if n := len(bot.callbacks); n > 2500 {
		t.Fatalf("callbacks map has %d entries after eviction", n)
	}
}

func TestGetChatMember(t *testing.T) {
	srv := New(Config{Quiet: true})
	_, resp := callBot(t, srv, "getChatMember", `{"chat_id":1,"user_id":321}`)
	m, _ := resp.Result.(map[string]any)
	user, _ := m["user"].(map[string]any)
	if m["status"] != "member" || user["id"] != float64(321) || user["is_bot"] != false {
		t.Fatalf("getChatMember result: %v", m)
	}
	if code, _ := callBot(t, srv, "getChatMember", `{`); code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", code)
	}
}

func TestDebugClearMessages(t *testing.T) {
	srv := New(Config{Quiet: true})
	callBot(t, srv, "sendMessage", sendTo("1"))
	callBot(t, srv, "sendMessage", sendTo("2"))

	rec := debugPOST(srv, "/debug/messages/903:faults/clear", ``)
	var out struct {
		Cleared int `json:"cleared"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || out.Cleared != 2 {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if msgs := srv.Store().Bot("903:faults").Messages(1, 10); len(msgs) != 0 {
		t.Fatalf("messages left after clear: %+v", msgs)
	}
}

func TestDebugProxyRegistration(t *testing.T) {
	srv := New(Config{Quiet: true})
	proxy := srv.Store().Proxy

	for _, body := range []string{
		`{`,
		`{"webhook_url":"http://p","chat_ids":[1]}`,
		`{"token":"t","webhook_url":"http://p","chat_ids":[]}`,
	} {
		if rec := debugPOST(srv, "/debug/proxy/register", body); rec.Code != http.StatusBadRequest {
			t.Errorf("register %s: status %d, want 400", body, rec.Code)
		}
	}
	if rec := debugPOST(srv, "/debug/proxy/unregister", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unregister without token: status %d, want 400", rec.Code)
	}

	rec := debugPOST(srv, "/debug/proxy/register", `{"token":"t","webhook_url":"http://p","chat_ids":[1,2,3]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"registered":3`) {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	if proxy.Lookup("t", 2) != "http://p" || proxy.Lookup("other", 2) != "" {
		t.Fatal("register did not bind chats to the token only")
	}

	debugPOST(srv, "/debug/proxy/unregister", `{"token":"t","chat_ids":[2]}`)
	if proxy.Lookup("t", 2) != "" || proxy.Lookup("t", 1) == "" {
		t.Fatal("unregister of one chat touched the wrong bindings")
	}
	debugPOST(srv, "/debug/proxy/unregister", `{"token":"t"}`)
	if proxy.Lookup("t", 1) != "" || proxy.Lookup("t", 3) != "" {
		t.Fatal("unregister without chat_ids kept bindings")
	}
}

func TestMaskToken(t *testing.T) {
	for in, want := range map[string]string{
		"12345:secret": "12345",
		"short":        "short",
		"abcdefghijkl": "abcdefgh",
		":leading":     ":leading",
	} {
		if got := maskToken(in); got != want {
			t.Errorf("maskToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetMeReturnsBotFullInfo(t *testing.T) {
	srv := New(Config{Quiet: true})
	code, resp := callBot(t, srv, "getMe", `{}`)
	if code != http.StatusOK {
		t.Fatalf("getMe: HTTP %d", code)
	}
	info, _ := resp.Result.(map[string]any)
	if info["id"] != float64(903) || info["is_bot"] != true || info["username"] != "telegym_mock_bot" {
		t.Fatalf("getMe = %v", info)
	}
	// Every required BotFullInfo field is present, even when false.
	spec := MustLoadAPISpec()
	for _, f := range spec.Types["BotFullInfo"].Fields {
		if _, ok := info[f.Name]; f.Required && !ok {
			t.Errorf("getMe lacks required field %s", f.Name)
		}
	}

	// Since Bot API 10.5 the capability flags live only in BotFullInfo.
	_, resp = callBot(t, srv, "sendMessage", `{"chat_id":1,"text":"hi"}`)
	from, _ := resp.Result.(map[string]any)["from"].(map[string]any)
	if from == nil || from["can_join_groups"] != nil {
		t.Fatalf("message from = %v", from)
	}
}
