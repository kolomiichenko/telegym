package mock

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestInjectUpdateDispatchObservability(t *testing.T) {
	for _, webhook := range []bool{false, true} {
		result := "queued"
		if webhook {
			result = "ok"
		}

		t.Run(result, func(t *testing.T) {
			cfg := Config{Quiet: true}
			wantStatus := 0
			if webhook {
				receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}))
				defer receiver.Close()
				cfg.DefaultWebhookURL = receiver.URL
				wantStatus = http.StatusOK
			}

			srv := New(cfg)
			if !webhook {
				startPolling(t, srv, "889:inject")
			}
			rec := pollingRequest(srv, http.MethodPost, "/debug/inject/update", `{"token":"889:inject","chat_id":42,"text":"hello"}`)
			var response struct {
				OK             bool  `json:"ok"`
				Queued         *bool `json:"queued"`
				DeliveryStatus int   `json:"delivery_status"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode inject response: %v", err)
			}
			if rec.Code != http.StatusOK || !response.OK || response.DeliveryStatus != wantStatus {
				t.Fatalf("unexpected inject response: status=%d body=%s", rec.Code, rec.Body.String())
			}
			if response.Queued == nil || *response.Queued != !webhook {
				t.Fatalf("expected queued=%t: %s", !webhook, rec.Body.String())
			}

			rec = httptest.NewRecorder()
			srv.metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			wantMetric := `telegym_mock_webhook_dispatches_total{result="` + result + `"} 1`
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), wantMetric) {
				t.Fatalf("expected metric %s: status=%d body=%s", wantMetric, rec.Code, rec.Body.String())
			}
			if !webhook && !strings.Contains(rec.Body.String(), "telegym_mock_webhook_dispatch_duration_seconds_count 0") {
				t.Fatal("queueing was counted as webhook delivery latency")
			}
		})
	}
}

// injectWebhook returns the update delivered by the injection endpoint.
func injectWebhook(t *testing.T, payload string, wantStatus int) *Update {
	t.Helper()
	received := make(chan []byte, 1)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	srv := New(Config{Quiet: true, DefaultWebhookURL: receiver.URL})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/inject/update", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("inject status=%d body=%s, want %d", rec.Code, rec.Body.String(), wantStatus)
	}

	// Delivery completes before the injection response is returned.
	select {
	case body := <-received:
		if wantStatus != http.StatusOK {
			t.Fatalf("rejected update reached webhook: %s", body)
		}
		var update Update
		if err := json.Unmarshal(body, &update); err != nil {
			t.Fatalf("decode webhook update: %v", err)
		}
		if update.UpdateID == 0 {
			t.Fatalf("webhook update lacks ID: %s", body)
		}
		return &update
	default:
		if wantStatus == http.StatusOK {
			t.Fatal("successful injection did not deliver a webhook update")
		}
		return nil
	}
}

func TestInjectPrivateMessageCompatibility(t *testing.T) {
	update := injectWebhook(t, `{"token":"889:inject","chat_id":42,"user_id":7,"first_name":"Alice","username":"alice","text":"hello"}`, http.StatusOK)
	message := update.Message
	if message == nil || update.EditedMessage != nil || update.CallbackQuery != nil || update.ChatMember != nil {
		t.Fatalf("unexpected update type: %+v", update)
	}
	if message.MessageID == 0 || message.Text != "hello" || message.From == nil || message.From.ID != 7 ||
		message.From.FirstName != "Alice" || message.From.Username != "alice" || message.From.IsBot {
		t.Fatalf("unexpected private message: %+v", message)
	}
	if message.Chat.ID != 42 || message.Chat.Type != "private" || message.Chat.FirstName != "Alice" || message.Chat.Username != "alice" {
		t.Fatalf("private chat identity changed: %+v", message.Chat)
	}
}

func TestInjectGroupMessages(t *testing.T) {
	for _, kind := range []string{"message", "edited_message"} {
		t.Run(kind, func(t *testing.T) {
			payload := `{
				"token":"889:inject", "type":"` + kind + `",
				"chat_id":-10042, "chat_type":"supergroup",
				"chat_title":"Test group", "user_id":7,
				"first_name":"Alice", "message_id":123, "text":"hello"
			}`
			update := injectWebhook(t, payload, http.StatusOK)
			message := update.Message
			if kind == "edited_message" {
				message = update.EditedMessage
				if update.Message != nil {
					t.Fatalf("edited update also contains a message: %+v", update)
				}
			}
			if message == nil || message.MessageID == 0 || message.Text != "hello" || message.Date == 0 ||
				message.From == nil || message.From.ID != 7 || message.From.IsBot {
				t.Fatalf("unexpected group message: %+v", message)
			}
			if kind == "edited_message" && message.MessageID != 123 {
				t.Fatalf("edit message ID=%d, want 123", message.MessageID)
			}
			if kind == "message" && message.MessageID == 123 {
				t.Fatal("ordinary message used the supplied ID instead of generating one")
			}
			if message.Chat.ID != -10042 || message.Chat.Type != "supergroup" || message.Chat.Title != "Test group" ||
				message.Chat.FirstName != "" || message.Chat.Username != "" {
				t.Fatalf("unexpected group chat: %+v", message.Chat)
			}
		})
	}
}

func TestInjectGroupCallback(t *testing.T) {
	for _, kind := range []string{"", "callback_query"} {
		t.Run("type="+kind, func(t *testing.T) {
			update := injectWebhook(t, `{"token":"889:inject","type":"`+kind+`","chat_id":-42,"chat_type":"group","chat_title":"Test group","user_id":7,"message_id":123,"callback_data":"next"}`, http.StatusOK)
			callback := update.CallbackQuery
			if callback == nil || callback.Data != "next" || callback.From.ID != 7 || callback.ID == "" || update.Message != nil {
				t.Fatalf("unexpected callback update: %+v", update)
			}
			message := callback.Message
			if message == nil || message.MessageID != 123 || message.From == nil || !message.From.IsBot ||
				message.Chat.ID != -42 || message.Chat.Type != "group" || message.Chat.Title != "Test group" {
				t.Fatalf("unexpected callback message: %+v", message)
			}
		})
	}
}

func TestInjectChatMember(t *testing.T) {
	for _, actorID := range []int64{0, 7, 9} {
		t.Run("actor="+strconv.FormatInt(actorID, 10), func(t *testing.T) {
			update := injectWebhook(t, `{"token":"889:inject","type":"chat_member","chat_id":-42,"chat_type":"supergroup","chat_title":"Test group","user_id":7,"first_name":"Alice","actor_id":`+strconv.FormatInt(actorID, 10)+`,"old_status":"left","new_status":"member"}`, http.StatusOK)
			member := update.ChatMember
			if member == nil || update.Message != nil || update.CallbackQuery != nil || update.EditedMessage != nil {
				t.Fatalf("unexpected member update: %+v", update)
			}
			if member.Chat.ID != -42 || member.Chat.Type != "supergroup" || member.Chat.Title != "Test group" || member.Date == 0 ||
				member.OldChatMember.Status != "left" || member.NewChatMember.Status != "member" ||
				member.OldChatMember.User.ID != 7 || member.NewChatMember.User.ID != 7 || member.NewChatMember.User.FirstName != "Alice" {
				t.Fatalf("unexpected member transition: %+v", member)
			}
			wantActor := actorID
			if wantActor == 0 {
				wantActor = 7
			}
			if member.From.ID != wantActor || member.From.FirstName == "" || (wantActor == 7 && member.From.FirstName != "Alice") {
				t.Fatalf("unexpected member actor: %+v", member.From)
			}
		})
	}
}

func TestInjectRejectsUnsupportedUpdates(t *testing.T) {
	tests := []struct {
		name   string
		fields string
	}{
		{"unknown type", `"type":"unknown"`},
		{"missing old status", `"type":"chat_member","new_status":"member"`},
		{"missing new status", `"type":"chat_member","old_status":"left"`},
		{"unsupported old status", `"type":"chat_member","old_status":"administrator","new_status":"member"`},
		{"unsupported new status", `"type":"chat_member","old_status":"member","new_status":"restricted"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			injectWebhook(t, `{"token":"889:inject","chat_id":-42,"user_id":7,`+tt.fields+`}`, http.StatusBadRequest)
		})
	}
}

const injectUser = `"token":"889:inject","chat_id":42,"user_id":7,"first_name":"Alice"`

func TestInjectMyChatMember(t *testing.T) {
	tests := []struct {
		name, old, new string
		wantUntilOld   bool
		wantUntilNew   bool
	}{
		{"block", "member", "kicked", false, true},
		{"unblock", "kicked", "member", true, false},
		{"leave", "member", "left", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			update := injectWebhook(t, `{`+injectUser+`,"type":"my_chat_member","old_status":"`+tt.old+`","new_status":"`+tt.new+`"}`, http.StatusOK)
			member := update.MyChatMember
			if member == nil || update.ChatMember != nil || update.Message != nil {
				t.Fatalf("unexpected update: %+v", update)
			}
			if member.From.ID != 7 || member.Date == 0 || member.Chat.ID != 42 || member.Chat.FirstName != "Alice" {
				t.Fatalf("unexpected actor or chat: %+v", member)
			}
			if !member.NewChatMember.User.IsBot || !member.OldChatMember.User.IsBot ||
				member.OldChatMember.Status != tt.old || member.NewChatMember.Status != tt.new {
				t.Fatalf("the bot should be the member that changed: %+v", member)
			}
			if (member.OldChatMember.UntilDate != nil) != tt.wantUntilOld || (member.NewChatMember.UntilDate != nil) != tt.wantUntilNew {
				t.Fatalf("until_date is required for kicked members only: %+v", member)
			}
		})
	}
}

func TestInjectInlineUpdates(t *testing.T) {
	update := injectWebhook(t, `{`+injectUser+`,"type":"inline_query","query":"cats","offset":"20"}`, http.StatusOK)
	query := update.InlineQuery
	if query == nil || query.ID == "" || query.From.ID != 7 || query.Query != "cats" || query.Offset != "20" || query.ChatType != "private" {
		t.Fatalf("unexpected inline query: %+v", update)
	}

	update = injectWebhook(t, `{`+injectUser+`,"type":"chosen_inline_result","result_id":"r1","query":"cats"}`, http.StatusOK)
	chosen := update.ChosenInlineResult
	if chosen == nil || chosen.ResultID != "r1" || chosen.From.ID != 7 || chosen.Query != "cats" {
		t.Fatalf("unexpected chosen result: %+v", update)
	}
}

func TestInjectPayments(t *testing.T) {
	tests := []struct {
		name, currency, want string
	}{
		{"stars by default", "", "XTR"},
		{"explicit currency", "USD", "USD"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := `{` + injectUser + `,"total_amount":150,"invoice_payload":"order-1","currency":"` + tt.currency + `",`

			update := injectWebhook(t, fields+`"type":"pre_checkout_query"}`, http.StatusOK)
			query := update.PreCheckoutQuery
			if query == nil || query.ID == "" || query.From.ID != 7 || query.Currency != tt.want ||
				query.TotalAmount != 150 || query.InvoicePayload != "order-1" {
				t.Fatalf("unexpected pre-checkout query: %+v", update)
			}

			update = injectWebhook(t, fields+`"type":"successful_payment"}`, http.StatusOK)
			if update.Message == nil || update.Message.MessageID == 0 || update.Message.From == nil || update.Message.Chat.ID != 42 {
				t.Fatalf("unexpected payment message: %+v", update)
			}
			payment := update.Message.SuccessfulPayment
			if payment == nil || payment.Currency != tt.want || payment.TotalAmount != 150 || payment.InvoicePayload != "order-1" ||
				payment.TelegramPaymentChargeID == "" || payment.ProviderPaymentChargeID == "" {
				t.Fatalf("unexpected payment: %+v", payment)
			}
		})
	}
}

func TestInjectMessageReaction(t *testing.T) {
	update := injectWebhook(t, `{`+injectUser+`,"type":"message_reaction","message_id":123,"new_reaction":["👍"]}`, http.StatusOK)
	reaction := update.MessageReaction
	if reaction == nil || reaction.MessageID != 123 || reaction.Date == 0 || reaction.Chat.ID != 42 || reaction.User == nil || reaction.User.ID != 7 {
		t.Fatalf("unexpected reaction: %+v", update)
	}
	if reaction.OldReaction == nil || len(reaction.OldReaction) != 0 {
		t.Fatalf("old_reaction must be an empty array: %+v", reaction.OldReaction)
	}
	if len(reaction.NewReaction) != 1 || reaction.NewReaction[0] != (ReactionType{Type: "emoji", Emoji: "👍"}) {
		t.Fatalf("unexpected new_reaction: %+v", reaction.NewReaction)
	}
}

func TestInjectPollAnswer(t *testing.T) {
	update := injectWebhook(t, `{`+injectUser+`,"type":"poll_answer","poll_id":"p1","option_ids":[0,2]}`, http.StatusOK)
	answer := update.PollAnswer
	if answer == nil || answer.PollID != "p1" || answer.User == nil || answer.User.ID != 7 {
		t.Fatalf("unexpected poll answer: %+v", update)
	}
	if len(answer.OptionIDs) != 2 || answer.OptionIDs[1] != 2 || len(answer.OptionPersistentIDs) != 2 {
		t.Fatalf("unexpected options: %+v", answer)
	}

	update = injectWebhook(t, `{`+injectUser+`,"type":"poll_answer","poll_id":"p1"}`, http.StatusOK)
	if update.PollAnswer.OptionIDs == nil || update.PollAnswer.OptionPersistentIDs == nil {
		t.Fatalf("a retracted vote must carry empty arrays: %+v", update.PollAnswer)
	}
}

func TestInjectMedia(t *testing.T) {
	t.Run("photo", func(t *testing.T) {
		message := injectWebhook(t, `{`+injectUser+`,"photo":[{"file_id":"ph1"}],"caption":"look"}`, http.StatusOK).Message
		if message == nil || len(message.Photo) != 1 || message.Caption != "look" || message.Text != "" {
			t.Fatalf("unexpected photo message: %+v", message)
		}
		if size := message.Photo[0]; size.FileID != "ph1" || size.FileUniqueID == "" || size.Width == 0 || size.Height == 0 {
			t.Fatalf("required photo fields not filled: %+v", size)
		}
	})
	t.Run("document", func(t *testing.T) {
		message := injectWebhook(t, `{`+injectUser+`,"document":{"file_id":"d1","file_name":"a.pdf"},"caption":"cv"}`, http.StatusOK).Message
		if message == nil || message.Document == nil || message.Document.FileUniqueID == "" || message.Document.FileName != "a.pdf" || message.Caption != "cv" {
			t.Fatalf("unexpected document message: %+v", message)
		}
	})
	t.Run("contact", func(t *testing.T) {
		message := injectWebhook(t, `{`+injectUser+`,"contact":{"phone_number":"+380501234567","user_id":7}}`, http.StatusOK).Message
		if message == nil || message.Contact == nil || message.Contact.PhoneNumber != "+380501234567" ||
			message.Contact.FirstName != "Alice" || message.Contact.UserID != 7 {
			t.Fatalf("unexpected contact message: %+v", message)
		}
	})
	t.Run("location", func(t *testing.T) {
		message := injectWebhook(t, `{`+injectUser+`,"location":{"latitude":50.45,"longitude":30.52}}`, http.StatusOK).Message
		if message == nil || message.Location == nil || message.Location.Latitude != 50.45 || message.Location.Longitude != 30.52 {
			t.Fatalf("unexpected location message: %+v", message)
		}
	})
	t.Run("sticker", func(t *testing.T) {
		message := injectWebhook(t, `{`+injectUser+`,"sticker":{"file_id":"st1","emoji":"🔥"}}`, http.StatusOK).Message
		if message == nil || message.Sticker == nil || message.Text != "" {
			t.Fatalf("unexpected sticker message: %+v", message)
		}
		if s := message.Sticker; s.FileID != "st1" || s.Emoji != "🔥" || s.FileUniqueID == "" || s.Type != "regular" || s.Width == 0 || s.Height == 0 {
			t.Fatalf("required sticker fields not filled: %+v", s)
		}
	})
	t.Run("edited", func(t *testing.T) {
		update := injectWebhook(t, `{`+injectUser+`,"type":"edited_message","message_id":5,"location":{"latitude":1,"longitude":2}}`, http.StatusOK)
		if update.EditedMessage == nil || update.EditedMessage.MessageID != 5 || update.EditedMessage.Location == nil {
			t.Fatalf("unexpected edited location: %+v", update)
		}
	})
}

// TestInjectQueryIDsReachPollingBots checks the ids a scenario needs to match
// the bot's answer, and that the new types are delivered by long polling too.
func TestInjectQueryIDsReachPollingBots(t *testing.T) {
	tests := []struct {
		name, fields, idKey string
		id                  func(Update) string
	}{
		{"inline query", `"type":"inline_query","query":"cats"`, "inline_query_id", func(u Update) string { return u.InlineQuery.ID }},
		{"pre-checkout", `"type":"pre_checkout_query","total_amount":5,"invoice_payload":"p"`, "pre_checkout_query_id", func(u Update) string { return u.PreCheckoutQuery.ID }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(Config{Quiet: true})
			startPolling(t, srv, "889:inject")
			rec := pollingRequest(srv, http.MethodPost, "/debug/inject/update", `{`+injectUser+`,`+tt.fields+`}`)
			var response map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("inject status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
			}

			updates := decodeUpdates(t, pollingRequest(srv, http.MethodGet, "/bot889:inject/getUpdates", ""))
			if len(updates) != 1 || tt.id(updates[0]) == "" || response[tt.idKey] != tt.id(updates[0]) {
				t.Fatalf("response %v does not name the delivered query: %+v", response, updates)
			}
		})
	}
}

func TestInjectRejectsInvalidUpdates(t *testing.T) {
	tests := []struct {
		name   string
		fields string
	}{
		{"my_chat_member without status", `"type":"my_chat_member","new_status":"kicked"`},
		{"my_chat_member administrator", `"type":"my_chat_member","old_status":"member","new_status":"administrator"`},
		{"chosen result without id", `"type":"chosen_inline_result","query":"cats"`},
		{"pre-checkout without amount", `"type":"pre_checkout_query","invoice_payload":"p"`},
		{"pre-checkout without payload", `"type":"pre_checkout_query","total_amount":5`},
		{"payment without amount", `"type":"successful_payment","invoice_payload":"p"`},
		{"reaction without message", `"type":"message_reaction","new_reaction":["👍"]`},
		{"reaction without emoji", `"type":"message_reaction","message_id":1`},
		{"poll answer without poll", `"type":"poll_answer","option_ids":[0]`},
		{"photo without file id", `"photo":[{"width":1}]`},
		{"document without file id", `"document":{"file_name":"a.pdf"}`},
		{"contact without phone", `"contact":{"first_name":"A"}`},
		{"sticker without file id", `"sticker":{"emoji":"🔥"}`},
		{"caption on a sticker", `"caption":"hi","sticker":{"file_id":"s"}`},
		{"text with a sticker", `"text":"hi","sticker":{"file_id":"s"}`},
		{"text with media", `"text":"hi","location":{"latitude":1,"longitude":2}`},
		{"two kinds of media", `"photo":[{"file_id":"p"}],"location":{"latitude":1,"longitude":2}`},
		{"caption without media", `"caption":"hi"`},
		{"caption on a contact", `"caption":"hi","contact":{"phone_number":"1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			injectWebhook(t, `{`+injectUser+`,`+tt.fields+`}`, http.StatusBadRequest)
		})
	}
}
