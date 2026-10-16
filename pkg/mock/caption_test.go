package mock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestEditMessageCaption(t *testing.T) {
	tests := []struct {
		name           string
		contentType    string
		body           string
		caption        string
		entities       []Entity
		markup         *ReplyMarkup
		status         int
		inline         bool
		initialCaption string
	}{
		{
			name:        "caption with entities and keyboard",
			contentType: "application/json",
			body:        `{"chat_id":42,"message_id":12,"caption":"updated","caption_entities":[{"type":"bold","offset":0,"length":7}],"reply_markup":{"inline_keyboard":[[{"text":"Next","callback_data":"next"}]]}}`,
			caption:     "updated",
			entities:    []Entity{{Type: "bold", Offset: 0, Length: 7}},
			markup:      &ReplyMarkup{InlineKeyboard: [][]InlineButton{{{Text: "Next", CallbackData: "next"}}}},
			status:      http.StatusOK,
		},
		{
			name:           "remove caption",
			initialCaption: "original",
			contentType:    "application/json",
			body:           `{"chat_id":42,"message_id":12}`,
			status:         http.StatusOK,
		},
		{
			name:        "inline message",
			contentType: "application/json",
			body:        `{"inline_message_id":"inline-12","caption":"updated"}`,
			status:      http.StatusOK,
			inline:      true,
		},
		{
			name:        "malformed body",
			contentType: "application/json",
			body:        `{"chat_id":`,
			status:      http.StatusBadRequest,
		},
	}

	for _, prefix := range []string{"/bot741:caption", "/bot741:caption/test"} {
		for _, tt := range tests {
			t.Run(prefix+"/"+tt.name, func(t *testing.T) {
				srv := New(Config{Quiet: true})
				if tt.initialCaption != "" {
					rec := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodPost, prefix+"/editMessageCaption", strings.NewReader(`{"chat_id":42,"message_id":12,"caption":"original"}`))
					req.Header.Set("Content-Type", "application/json")
					srv.Handler().ServeHTTP(rec, req)
					if rec.Code != http.StatusOK {
						t.Fatalf("initial edit status=%d body=%s", rec.Code, rec.Body.String())
					}
				}

				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, prefix+"/editMessageCaption", strings.NewReader(tt.body))
				req.Header.Set("Content-Type", tt.contentType)
				srv.Handler().ServeHTTP(rec, req)
				if rec.Code != tt.status {
					t.Fatalf("edit status=%d body=%s, want %d", rec.Code, rec.Body.String(), tt.status)
				}

				var response struct {
					OK     bool            `json:"ok"`
					Result json.RawMessage `json:"result"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode edit response: %v", err)
				}
				if response.OK != (tt.status == http.StatusOK) {
					t.Fatalf("unexpected edit response: %s", rec.Body.String())
				}
				if tt.inline && string(response.Result) != "true" {
					t.Fatalf("inline edit result=%s, want true", response.Result)
				}

				wantCount := 0
				if tt.status == http.StatusOK && !tt.inline {
					wantCount = 1
					if tt.initialCaption != "" {
						wantCount = 2
					}
					var message Message
					if err := json.Unmarshal(response.Result, &message); err != nil {
						t.Fatalf("decode edited message: %v", err)
					}
					if message.MessageID != 12 || message.Chat.ID != 42 || message.Caption != tt.caption ||
						!reflect.DeepEqual(message.CaptionEntities, tt.entities) || !reflect.DeepEqual(message.ReplyMarkup, tt.markup) {
						t.Fatalf("unexpected edited message: %+v", message)
					}
				}

				rec = httptest.NewRecorder()
				req = httptest.NewRequest(http.MethodGet, "/debug/messages/741:caption?chat_id=42", nil)
				srv.Handler().ServeHTTP(rec, req)
				var debug struct {
					Messages []Message `json:"messages"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &debug); err != nil {
					t.Fatalf("decode debug messages: %v", err)
				}
				if rec.Code != http.StatusOK || len(debug.Messages) != wantCount {
					t.Fatalf("debug messages status=%d body=%s, want %d messages", rec.Code, rec.Body.String(), wantCount)
				}
				if wantCount > 0 {
					message := debug.Messages[0]
					if message.MessageID != 12 || message.Caption != tt.caption || message.EventSeq == 0 ||
						!reflect.DeepEqual(message.CaptionEntities, tt.entities) || !reflect.DeepEqual(message.ReplyMarkup, tt.markup) {
						t.Fatalf("unexpected recorded edit: %+v", message)
					}
					if wantCount == 2 && (debug.Messages[1].Caption != "original" || message.EventSeq <= debug.Messages[1].EventSeq) {
						t.Fatalf("caption removal did not record a new event: %+v", debug.Messages)
					}
				}
			})
		}
	}
}
