package mock

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

func TestReplyParametersResolveStoredMessage(t *testing.T) {
	srv := New(Config{Quiet: true})
	send := func(method, body string) (int, Message) {
		t.Helper()
		rec := doRichTestPOST(t, srv, "/bot741:reply/"+method, body)
		var response struct {
			Result Message `json:"result"`
		}
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, response.Result
	}

	_, question := send("sendMessage", `{"chat_id":42,"text":"question"}`)
	_, answer := send("sendMessage", `{"chat_id":42,"text":"answer","reply_parameters":{"message_id":`+strconv.Itoa(question.MessageID)+`}}`)
	if r := answer.ReplyToMessage; r == nil || r.MessageID != question.MessageID || r.Text != "question" || r.From == nil {
		t.Fatalf("sendMessage reply not resolved from history: %+v", r)
	}

	for _, tc := range []struct{ method, body string }{
		{"sendPhoto", `{"chat_id":42,"photo":"file-1"}`},
		{"sendDice", `{"chat_id":42}`},
		{"sendRichMessage", `{"chat_id":42,"rich_message":{"html":"hi"}}`},
	} {
		body := tc.body[:len(tc.body)-1] + `,"reply_parameters":{"message_id":` + strconv.Itoa(answer.MessageID) + `}}`
		code, m := send(tc.method, body)
		r := m.ReplyToMessage
		if code != http.StatusOK || r == nil || r.Text != "answer" || r.ReplyToMessage != nil || r.EventSeq != 0 {
			t.Fatalf("%s: status=%d reply=%+v", tc.method, code, r)
		}
	}

	// User messages are not stored, so the reply keeps only the ID.
	_, m := send("sendMessage", `{"chat_id":42,"text":"hi","reply_parameters":{"message_id":999}}`)
	if r := m.ReplyToMessage; r == nil || r.MessageID != 999 || r.Chat.ID != 42 || r.Text != "" {
		t.Fatalf("unknown reply target: %+v", r)
	}

	for _, method := range []string{"sendMessage", "sendPhoto", "sendDice"} {
		if code, _ := send(method, `{"chat_id":42,"reply_parameters":"{"}`); code != http.StatusBadRequest {
			t.Fatalf("%s accepted malformed reply_parameters: status=%d", method, code)
		}
	}
}

func TestRichMediaRejectedMessageDropsStoredUploads(t *testing.T) {
	srv := New(Config{Quiet: true})
	rec := postRichMedia(t, srv, `{"blocks":[
		{"type":"photo","photo":{"type":"photo","media":"attach://picture"}},
		{"type":"photo","photo":{"type":"photo","media":"attach://missing"}}
	]}`, richTestUpload{"picture", "photo.jpg", "image/jpeg", "bytes"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if count, bytes := srv.Store().Files.Stats(); count != 0 || bytes != 0 {
		t.Fatalf("rejected message kept uploads: count=%d bytes=%d", count, bytes)
	}
}

func TestRichMediaSharedAttachmentStoredOnce(t *testing.T) {
	srv := New(Config{Quiet: true})
	rec := postRichMedia(t, srv, `{"blocks":[
		{"type":"photo","photo":{"type":"photo","media":"attach://picture"}},
		{"type":"photo","photo":{"type":"photo","media":"attach://picture"}}
	]}`, richTestUpload{"picture", "photo.jpg", "image/jpeg", "bytes"})
	blocks := richResultBlocks(t, richMediaMessage(t, rec))
	if len(blocks) != 2 || blocks[0].Photo[0].FileID != blocks[1].Photo[0].FileID {
		t.Fatalf("shared attachment got different file IDs: %s", rec.Body.String())
	}
	if count, _ := srv.Store().Files.Stats(); count != 1 {
		t.Fatalf("shared attachment stored %d times", count)
	}
}
