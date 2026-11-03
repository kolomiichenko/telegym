package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"reflect"
	"strings"
	"testing"
)

type richTestUpload struct {
	part, filename, contentType, data string
}

func postRichMedia(t *testing.T, srv *Server, richMessage string, uploads ...richTestUpload) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for key, value := range map[string]string{"chat_id": "42", "rich_message": richMessage} {
		if err := w.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, upload := range uploads {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, upload.part, upload.filename))
		header.Set("Content-Type", upload.contentType)
		part, err := w.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(upload.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/bot741:rich/sendRichMessage", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	t.Cleanup(func() {
		if req.MultipartForm != nil {
			if err := req.MultipartForm.RemoveAll(); err != nil {
				t.Error(err)
			}
		}
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func richMediaMessage(t *testing.T, rec *httptest.ResponseRecorder) Message {
	t.Helper()
	var response struct {
		OK     bool    `json:"ok"`
		Result Message `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || !response.OK || response.Result.RichMessage == nil {
		t.Fatalf("unexpected rich response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	return response.Result
}

func richMediaHistory(t *testing.T, srv *Server) []Message {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/messages/741:rich?chat_id=42", nil))
	var response struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("debug messages status=%d body=%s", rec.Code, rec.Body.String())
	}
	return response.Messages
}

type richTestBlock struct {
	Type      string          `json:"type"`
	Photo     []PhotoSize     `json:"photo"`
	Video     *Video          `json:"video"`
	Animation *Animation      `json:"animation"`
	Blocks    []richTestBlock `json:"blocks"`
	Text      string          `json:"text"`
	Caption   struct {
		Text string `json:"text"`
	} `json:"caption"`
}

func richResultBlocks(t *testing.T, message Message) []richTestBlock {
	t.Helper()
	blocks := make([]richTestBlock, len(message.RichMessage.Blocks))
	for i, raw := range message.RichMessage.Blocks {
		if err := json.Unmarshal(raw, &blocks[i]); err != nil {
			t.Fatal(err)
		}
	}
	return blocks
}

func TestRichMediaUploadsAndCache(t *testing.T) {
	srv := New(Config{Quiet: true})
	uploads := []richTestUpload{
		{"picture", "photo.jpg", "image/jpeg", "\xff\xd8\xffphoto bytes"},
		{"clip", "video.mp4", "video/mp4", "video bytes"},
		{"gif", "animation.gif", "image/gif", "GIF89a"},
	}
	rec := postRichMedia(t, srv, `{"is_rtl":true,"blocks":[
		{"type":"collage","blocks":[
			{"type":"photo","photo":{"type":"photo","media":"attach://picture"},"caption":{"text":"first"}},
			{"type":"video","video":{"type":"video","media":"attach://clip","width":640,"height":360,"duration":7}},
			{"type":"photo","photo":{"type":"photo","media":"cached-photo"}}
		]},
		{"type":"animation","animation":{"type":"animation","media":"attach://gif","width":32,"height":24,"duration":2}},
		{"type":"paragraph","text":"description"}
	]}`, uploads...)
	message := richMediaMessage(t, rec)
	blocks := richResultBlocks(t, message)
	if !message.RichMessage.IsRTL || len(blocks) != 3 || blocks[0].Type != "collage" || len(blocks[0].Blocks) != 3 {
		t.Fatalf("unexpected rich message: %s", rec.Body.String())
	}
	collage := blocks[0].Blocks
	if collage[0].Type != "photo" || len(collage[0].Photo) != 1 || collage[0].Caption.Text != "first" ||
		collage[1].Type != "video" || collage[1].Video == nil ||
		collage[2].Type != "photo" || len(collage[2].Photo) != 1 || collage[2].Photo[0].FileID != "cached-photo" {
		t.Fatalf("collage order or content changed: %s", rec.Body.String())
	}
	photo := collage[0].Photo[0]
	video := collage[1].Video
	if photo.FileSize != len(uploads[0].data) || photo.FileUniqueID == "" ||
		video.Width != 640 || video.Height != 360 || video.Duration != 7 ||
		video.FileName != uploads[1].filename || video.MimeType != uploads[1].contentType || video.FileSize != int64(len(uploads[1].data)) {
		t.Fatalf("missing photo/video metadata: %s", rec.Body.String())
	}
	animation := blocks[1].Animation
	if blocks[1].Type != "animation" || animation == nil || animation.Width != 32 || animation.Height != 24 || animation.Duration != 2 ||
		animation.FileName != uploads[2].filename || animation.MimeType != uploads[2].contentType || animation.FileSize != int64(len(uploads[2].data)) ||
		blocks[2].Type != "paragraph" || blocks[2].Text != "description" {
		t.Fatalf("animation or text changed: %s", rec.Body.String())
	}
	ids := []string{photo.FileID, video.FileID, animation.FileID}
	for i, id := range ids {
		file := httptest.NewRecorder()
		srv.Handler().ServeHTTP(file, httptest.NewRequest(http.MethodGet, "/debug/files/"+id, nil))
		if file.Code != http.StatusOK || file.Body.String() != uploads[i].data ||
			file.Header().Get("Content-Type") != uploads[i].contentType || !strings.Contains(file.Header().Get("Content-Disposition"), uploads[i].filename) {
			t.Fatalf("upload %s not captured: status=%d headers=%v body=%s", id, file.Code, file.Header(), file.Body.String())
		}
	}
	history := richMediaHistory(t, srv)
	if len(history) != 1 || !reflect.DeepEqual(history[0].RichMessage, message.RichMessage) {
		t.Fatalf("recorded rich media differ from response: %+v", history)
	}

	rec = doRichTestPOST(t, srv, "/bot741:rich/sendRichMessage", fmt.Sprintf(`{"chat_id":42,"rich_message":{"blocks":[
		{"type":"photo","photo":{"type":"photo","media":%q}},
		{"type":"video","video":{"type":"video","media":%q,"duration":0}},
		{"type":"animation","animation":{"type":"animation","media":%q,"duration":0}}
	]}}`, ids[0], ids[1], ids[2]))
	cached := richResultBlocks(t, richMediaMessage(t, rec))
	if len(cached) != 3 || len(cached[0].Photo) != 1 || cached[1].Video == nil || cached[2].Animation == nil ||
		cached[0].Photo[0].FileID != ids[0] || cached[1].Video.FileID != ids[1] || cached[2].Animation.FileID != ids[2] {
		t.Fatalf("cached media IDs changed: %s", rec.Body.String())
	}
	if cached[1].Video.Duration != 0 || cached[2].Animation.Duration != 0 {
		t.Fatalf("zero media duration changed: %s", rec.Body.String())
	}
	if count, _ := srv.Store().Files.Stats(); count != len(uploads) {
		t.Fatalf("cached send stored new uploads: count=%d", count)
	}
}

func TestRichMediaNestedContainers(t *testing.T) {
	for _, container := range []string{
		`{"type":"details","summary":"more","blocks":[%s]}`,
		`{"type":"list","items":[{"blocks":[%s]}]}`,
	} {
		srv := New(Config{Quiet: true})
		input := fmt.Sprintf(container, `{"type":"video","video":{"type":"video","media":"cached-video","width":640,"height":360,"duration":7}}`)
		rec := doRichTestPOST(t, srv, "/bot741:rich/sendRichMessage", `{"chat_id":42,"rich_message":{"blocks":[`+input+`]}}`)
		message := richMediaMessage(t, rec)
		want := fmt.Sprintf(container, `{"type":"video","video":{"file_id":"cached-video","file_unique_id":"cached-video_u","width":640,"height":360,"duration":7}}`)
		var gotBlock, wantBlock any
		if err := json.Unmarshal(message.RichMessage.Blocks[0], &gotBlock); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want), &wantBlock); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotBlock, wantBlock) {
			t.Fatalf("nested media not resolved: got=%s want=%s", message.RichMessage.Blocks[0], want)
		}
	}
}

func TestRichMediaRejectsInvalidUploads(t *testing.T) {
	for _, tc := range []struct {
		name, media, description string
		status                   int
	}{
		{"missing media", `null`, "requires photo input media", http.StatusBadRequest},
		{"wrong type", `{"type":"video","media":"cached"}`, "requires photo input media", http.StatusBadRequest},
		{"empty reference", `{"type":"photo","media":""}`, "media reference is required", http.StatusBadRequest},
		{"empty attachment", `{"type":"photo","media":"attach://"}`, "empty attach reference", http.StatusBadRequest},
		{"missing attachment", `{"type":"photo","media":"attach://missing"}`, "not found", http.StatusBadRequest},
		{"over capacity", `{"type":"photo","media":"attach://picture"}`, "file store capacity", http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := New(Config{Quiet: true, FileStoreMaxBytes: 3})
			richMessage := `{"blocks":[{"type":"photo","photo":` + tc.media + `}]}`
			rec := postRichMedia(t, srv, richMessage, richTestUpload{"picture", "photo.jpg", "image/jpeg", "four"})
			var response APIResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status || response.OK || response.ErrorCode != tc.status || !strings.Contains(response.Description, tc.description) {
				t.Fatalf("status=%d body=%s, want %d with %q", rec.Code, rec.Body.String(), tc.status, tc.description)
			}
			if history := richMediaHistory(t, srv); len(history) != 0 {
				t.Fatalf("rejected rich message was recorded: %+v", history)
			}
			if count, _ := srv.Store().Files.Stats(); count != 0 {
				t.Fatalf("rejected upload retained files: count=%d", count)
			}
		})
	}
}

func TestRichMediaCountLimit(t *testing.T) {
	for _, count := range []int{50, 51} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			srv := New(Config{Quiet: true})
			block := `{"type":"photo","photo":{"type":"photo","media":"cached-photo"}}`
			blocks := strings.TrimSuffix(strings.Repeat(block+",", count), ",")
			rec := doRichTestPOST(t, srv, "/bot741:rich/sendRichMessage", `{"chat_id":42,"rich_message":{"blocks":[`+blocks+`]}}`)
			if count == 50 {
				if message := richMediaMessage(t, rec); len(message.RichMessage.Blocks) != count {
					t.Fatalf("media at limit were dropped: %s", rec.Body.String())
				}
			} else if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at most 50 media") {
				t.Fatalf("media over limit accepted: status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
