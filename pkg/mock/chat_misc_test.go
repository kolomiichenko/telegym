package mock

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWidgetRegistryPrune(t *testing.T) {
	now := time.Now()
	var r widgetRegistry
	r.queries = map[string]*pendingQuery{}
	r.sent = map[int64][]time.Time{}
	for i := range maxWidgetQueries {
		r.queries["old"+strconv.Itoa(i)] = &pendingQuery{issued: now.Add(-widgetQueryTTL - time.Second)}
		r.sent[int64(i)] = []time.Time{now.Add(-widgetQueryWindow)}
	}
	r.queries["fresh"] = &pendingQuery{issued: now}
	r.sent[-1] = []time.Time{now}

	r.registerQuery("new", now)
	if len(r.queries) != 2 || r.queries["fresh"] == nil || r.queries["new"] == nil {
		t.Fatalf("queries after prune: %d", len(r.queries))
	}
	if len(r.sent) != 1 || r.sent[-1] == nil {
		t.Fatalf("users after prune: %d", len(r.sent))
	}

	// Pruning runs at most once per window even when the maps refill.
	for i := range maxWidgetQueries {
		r.queries["again"+strconv.Itoa(i)] = &pendingQuery{issued: now.Add(-widgetQueryTTL - time.Second)}
	}
	r.registerQuery("soon", now.Add(time.Second))
	if len(r.queries) < maxWidgetQueries {
		t.Fatal("prune ran again inside the throttle window")
	}
	r.registerQuery("later", now.Add(widgetQueryWindow))
	if len(r.queries) != 4 { // fresh, new, soon, later
		t.Fatalf("queries after second prune: %d, want 4", len(r.queries))
	}
}

func TestCurrentReplyKeyboard(t *testing.T) {
	keyboard := &ReplyMarkup{
		Keyboard:              [][]KeyboardButton{{{Text: "a"}, {Text: "b"}}, {{Text: "c"}}},
		InputFieldPlaceholder: "pick",
	}
	inline := &ReplyMarkup{InlineKeyboard: [][]InlineButton{{{Text: "i"}}}}
	remove := &ReplyMarkup{RemoveKeyboard: true}

	tests := []struct {
		name        string
		msgs        []Message // newest first
		rows        [][]string
		placeholder string
	}{
		{"none", []Message{{Text: "x"}}, nil, ""},
		{"keyboard", []Message{{}, {ReplyMarkup: inline}, {ReplyMarkup: keyboard}}, [][]string{{"a", "b"}, {"c"}}, "pick"},
		{"removed", []Message{{ReplyMarkup: remove}, {ReplyMarkup: keyboard}}, nil, ""},
		{"inline only", []Message{{ReplyMarkup: inline}}, nil, ""},
	}
	for _, tt := range tests {
		rows, placeholder := currentReplyKeyboard(tt.msgs)
		if !reflect.DeepEqual(rows, tt.rows) || placeholder != tt.placeholder {
			t.Errorf("%s: got %v %q, want %v %q", tt.name, rows, placeholder, tt.rows, tt.placeholder)
		}
	}
}

func TestMediaTag(t *testing.T) {
	for want, m := range map[string]Message{
		"photo":     {Photo: []PhotoSize{{}}},
		"video":     {Video: &Video{}},
		"animation": {Animation: &Animation{}},
		"sticker":   {Sticker: &Sticker{}},
		"dice":      {Dice: &Dice{}},
		"":          {Text: "plain"},
	} {
		if got := mediaTag(m); got != want {
			t.Errorf("mediaTag = %q, want %q", got, want)
		}
	}
}

func TestDebugChatHomeAndOpen(t *testing.T) {
	srv := New(Config{Quiet: true})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	callBot(t, srv, "getMe", `{}`) // make the bot show up in the inventory
	home := get("/debug/chat")
	if home.Code != http.StatusOK || !strings.Contains(home.Body.String(), "903:faults") {
		t.Fatalf("home: %d, bot missing from inventory", home.Code)
	}

	for path, want := range map[string]string{
		"/debug/chat/open?token=1:a&chat_id=5&name=bob": "/debug/chat/1:a/5?name=bob",
		"/debug/chat/open?token=1:a&chat_id=5":          "/debug/chat/1:a/5?name=andrii",
		"/debug/chat/open?token=1:a":                    "/debug/chat",
	} {
		rec := get(path)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d → %q, want %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
	}

	for _, path := range []string{"/debug/chat/static/htmx.min.js", "/debug/chat/static/idiomorph-ext.min.js"} {
		rec := get(path)
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 || rec.Header().Get("Content-Type") != "application/javascript" {
			t.Errorf("%s: %d, %d bytes, %q", path, rec.Code, rec.Body.Len(), rec.Header().Get("Content-Type"))
		}
	}
}
