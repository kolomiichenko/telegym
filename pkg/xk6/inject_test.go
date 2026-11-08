package telegym

import (
	"reflect"
	"testing"
)

func TestUserInjectHelpers(t *testing.T) {
	tests := []struct {
		name string
		call func(*User) error
		want map[string]any
	}{
		{"block", (*User).BlockBot, map[string]any{"type": "my_chat_member", "old_status": "member", "new_status": "kicked"}},
		{"unblock", (*User).UnblockBot, map[string]any{"type": "my_chat_member", "old_status": "kicked", "new_status": "member"}},
		{
			"inline query",
			func(u *User) error { _, err := u.InlineQuery("cats"); return err },
			map[string]any{"type": "inline_query", "query": "cats"},
		},
		{
			"chosen result",
			func(u *User) error { return u.ChooseInlineResult("r1", "cats") },
			map[string]any{"type": "chosen_inline_result", "result_id": "r1", "query": "cats"},
		},
		{
			"pre-checkout",
			func(u *User) error { _, err := u.PreCheckout("order", 5, ""); return err },
			map[string]any{"type": "pre_checkout_query", "invoice_payload": "order", "total_amount": float64(5), "currency": ""},
		},
		{
			"payment",
			func(u *User) error { return u.Pay("order", 5, "USD") },
			map[string]any{"type": "successful_payment", "invoice_payload": "order", "total_amount": float64(5), "currency": "USD"},
		},
		{
			"reaction targets the last matched message",
			func(u *User) error { u.lastSeenMessageID = 77; return u.React("👍") },
			map[string]any{"type": "message_reaction", "message_id": float64(77), "new_reaction": []any{"👍"}},
		},
		{
			"poll answer",
			func(u *User) error { return u.AnswerPoll("p1", []int{1}) },
			map[string]any{"type": "poll_answer", "poll_id": "p1", "option_ids": []any{float64(1)}},
		},
		{
			"photo",
			func(u *User) error { return u.SendPhoto("ph1", "look") },
			map[string]any{"type": "message", "photo": []any{map[string]any{"file_id": "ph1"}}, "caption": "look"},
		},
		{
			"document",
			func(u *User) error { return u.SendDocument("d1", "a.pdf", "cv") },
			map[string]any{"type": "message", "document": map[string]any{"file_id": "d1", "file_name": "a.pdf"}, "caption": "cv"},
		},
		{
			"contact shares the user's own number",
			func(u *User) error { return u.SendContact("+380501234567") },
			map[string]any{"type": "message", "contact": map[string]any{"phone_number": "+380501234567", "user_id": float64(9)}},
		},
		{
			"sticker",
			func(u *User) error { return u.SendSticker("st1", "🔥") },
			map[string]any{"type": "message", "sticker": map[string]any{"file_id": "st1", "emoji": "🔥"}},
		},
		{
			"location",
			func(u *User) error { return u.SendLocation(50.45, 30.52) },
			map[string]any{"type": "message", "location": map[string]any{"latitude": 50.45, "longitude": 30.52}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, c := newFakeMock(t)
			if err := tt.call(c.NewUser(9, nil)); err != nil {
				t.Fatalf("helper failed: %v", err)
			}
			got := m.lastInjected()
			for key, want := range tt.want {
				if !reflect.DeepEqual(got[key], want) {
					t.Errorf("%s = %#v, want %#v", key, got[key], want)
				}
			}
			if got["token"] != "42:test" || got["chat_id"] != float64(9) || got["user_id"] != float64(9) {
				t.Errorf("update is not attributed to the user: %v", got)
			}
		})
	}
}

// TestAwaitRequestScopedToInjectedQueries extends the VU isolation contract to
// answers that carry neither a chat_id nor a callback_query_id.
func TestAwaitRequestScopedToInjectedQueries(t *testing.T) {
	tests := []struct {
		name, responseKey, param, method string
		inject                           func(*User) (string, error)
	}{
		{
			"inline", "inline_query_id", "inline_query_id", "answerInlineQuery",
			func(u *User) (string, error) { return u.InlineQuery("cats") },
		},
		{
			"pre-checkout", "pre_checkout_query_id", "pre_checkout_query_id", "answerPreCheckoutQuery",
			func(u *User) (string, error) { return u.PreCheckout("order", 5, "") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, c := newFakeMock(t)
			m.injectFields = map[string]any{tt.responseKey: "mine"}
			u := c.NewUser(1, nil)
			id, err := tt.inject(u)
			if err != nil || id != "mine" {
				t.Fatalf("inject = %q, %v, want the id from the mock", id, err)
			}

			m.setRequests(journalEntry(1, tt.method, map[string]any{tt.param: "theirs"}))
			if _, err := u.AwaitRequest(tt.method, nil, 0.05); err == nil {
				t.Error("matched an answer to another user's query")
			}

			m.setRequests(journalEntry(2, tt.method, map[string]any{tt.param: "mine"}))
			if _, err := u.AwaitRequest(tt.method, nil, 1); err != nil {
				t.Errorf("did not match the answer to this user's query: %v", err)
			}
		})
	}
}
