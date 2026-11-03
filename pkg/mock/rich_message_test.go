package mock

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doRichTestPOST(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSendRichMessageValidation(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
	}{
		{"html", `{"chat_id":42,"rich_message":{"html":"<p>hi</p>"}}`, http.StatusOK},
		{"markdown", `{"chat_id":42,"rich_message":{"markdown":"# hi"}}`, http.StatusOK},
		{"blocks", `{"chat_id":42,"rich_message":{"blocks":[{"type":"paragraph","text":"hi"}]}}`, http.StatusOK},
		{"two sources", `{"chat_id":42,"rich_message":{"html":"<p>a</p>","markdown":"b"}}`, http.StatusBadRequest},
		{"empty rich_message", `{"chat_id":42,"rich_message":{}}`, http.StatusBadRequest},
		{"missing rich_message", `{"chat_id":42}`, http.StatusBadRequest},
		{"missing chat_id", `{"rich_message":{"html":"<p>a</p>"}}`, http.StatusBadRequest},
		{"malformed body", `{"chat_id":`, http.StatusBadRequest},
		{"malformed reply parameters", `{"chat_id":42,"rich_message":{"html":"hi"},"reply_parameters":"{"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(Config{Quiet: true})
			rec := doRichTestPOST(t, srv, "/bot741:rich/sendRichMessage", tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tt.status, rec.Body.String())
			}
		})
	}
}

func TestSendRichMessageResult(t *testing.T) {
	srv := New(Config{Quiet: true})
	rec := doRichTestPOST(t, srv, "/bot741:rich/sendRichMessage",
		`{"chat_id":42,"rich_message":{"blocks":[{"type":"paragraph","text":"hi"}],"is_rtl":true}}`)
	var env struct {
		Result struct {
			MessageID      int      `json:"message_id"`
			ReplyToMessage *Message `json:"reply_to_message"`
			RichMessage    struct {
				Blocks []map[string]any `json:"blocks"`
				IsRTL  bool             `json:"is_rtl"`
			} `json:"rich_message"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Result.MessageID == 0 || len(env.Result.RichMessage.Blocks) != 1 || !env.Result.RichMessage.IsRTL || env.Result.ReplyToMessage != nil {
		t.Fatalf("unexpected result %s", rec.Body.String())
	}

	rec = doRichTestPOST(t, srv, "/bot741:rich/sendRichMessage", `{"chat_id":42,"rich_message":{"html":"<p>hi</p>"}}`)
	if !strings.Contains(rec.Body.String(), `"rich_message":{"blocks":[]}`) {
		t.Fatalf("html input must return an empty block list, got %s", rec.Body.String())
	}
}

func TestSendRichMessageReply(t *testing.T) {
	for _, format := range []string{"json", "multipart"} {
		t.Run(format, func(t *testing.T) {
			const token = "741:rich"
			srv := New(Config{Quiet: true})
			body := bytes.NewBufferString(`{"chat_id":42,"rich_message":{"html":"hi"},"reply_parameters":{"message_id":910001}}`)
			contentType := "application/json"
			if format == "multipart" {
				body.Reset()
				w := multipart.NewWriter(body)
				for key, value := range map[string]string{
					"chat_id":          "42",
					"rich_message":     `{"html":"hi"}`,
					"reply_parameters": `{"message_id":910001}`,
				} {
					if err := w.WriteField(key, value); err != nil {
						t.Fatal(err)
					}
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				contentType = w.FormDataContentType()
			}
			req := httptest.NewRequest(http.MethodPost, "/bot"+token+"/sendRichMessage", body)
			req.Header.Set("Content-Type", contentType)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var response struct {
				Result Message `json:"result"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			assertRichReply(t, response.Result)

			rec = httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/messages/"+token+"?chat_id=42", nil))
			var history struct {
				Messages []Message `json:"messages"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusOK || len(history.Messages) != 1 {
				t.Fatalf("unexpected history: status=%d body=%s", rec.Code, rec.Body.String())
			}
			assertRichReply(t, history.Messages[0])
		})
	}
}

func assertRichReply(t *testing.T, message Message) {
	t.Helper()
	if message.RichMessage == nil || message.ReplyToMessage == nil {
		t.Fatalf("missing rich message or reply: %+v", message)
	}
	if reply := message.ReplyToMessage; reply.MessageID != 910001 || reply.Chat.ID != 42 {
		t.Fatalf("unexpected reply target: %+v", reply)
	}
}

func TestRenderRichHTML(t *testing.T) {
	in := `<h2>Verbs</h2><p>Present <mark>tense</mark> x<sup>2</sup></p>
<table bordered striped><caption>Forms</caption><tr><th>Person</th><th colspan="2">Form</th></tr>
<tr><td align="center" valign="top" onclick="x()">ես</td><td colspan="0">եմ</td></tr></table>
<ul><li><input type="checkbox" checked>done</li></ul><ol start="3" type="a" reversed><li value="7">x</li></ol>
<details open><summary>More</summary>Hidden</details><hr/><img src="https://example.com/a.jpg"/>
<tg-button type="callback_data" data="x">Go</tg-button><script>alert(1)</script><a href="#note">ref</a>`
	got := string(renderRichMessage(&RichMessage{HTML: in}, "tok", 1, "n"))
	for _, want := range []string{
		`<div class="rich">`, "<h2>Verbs</h2>", "<mark>tense</mark>", "<sup>2</sup>",
		`<div class="rich-table-wrap"><table class="rich-table bordered striped">`,
		"<caption>Forms</caption>", `<th colspan="2">Form</th>`, `<td align="center" valign="top">ես</td>`,
		"<td>եմ</td>", `<input type="checkbox" checked disabled>`, `<ol start="3" type="a" reversed>`,
		`<li value="7">`, "<details open><summary>More</summary>Hidden</details>", "<hr>",
		`<span class="media-tag">photo</span>`, `<span class="rich-button">Go</span>`, `<a href="#note">ref</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	for _, bad := range []string{"<script", "onclick", `colspan="0"`, "<br>"} {
		if strings.Contains(got, bad) {
			t.Errorf("unexpected %q in %s", bad, got)
		}
	}
}

func TestRenderRichBlocks(t *testing.T) {
	raw := `[
		{"type":"heading","size":9,"text":"Title"},
		{"type":"paragraph","text":["Hello ",{"type":"bold","text":"bold"}," ",{"type":"url","text":"link","url":"https://t.me"}," ",{"type":"url","text":"bad","url":"javascript:x"}]},
		{"type":"table","is_bordered":true,"is_compact":true,"caption":"Cap","cells":[
			[{"text":"H","is_header":true},{"text":"Wide","colspan":2,"align":"right"}],
			[{"text":"<b>raw</b>"},{"text":{"type":"spoiler","text":"s"}}]]},
		{"type":"list","items":[{"label":"1.","blocks":[{"type":"paragraph","text":"one"}]},{"label":"•","has_checkbox":true,"is_checked":true,"blocks":[]}]},
		{"type":"details","summary":"Sum","blocks":[{"type":"pre","text":"x := 1"}]},
		{"type":"photo","caption":{"text":"Pic"}}
	]`
	var blocks []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		t.Fatal(err)
	}
	got := string(renderRichMessage(&RichMessage{Blocks: blocks}, "tok", 1, "n"))
	for _, want := range []string{
		"<h6>Title</h6>", "<p>Hello <b>bold</b> ", `<a href="https://t.me" target="_blank" rel="noopener noreferrer">link</a>`,
		`<table class="rich-table bordered compact">`, "<caption>Cap</caption>", "<th>H</th>",
		`<td colspan="2" align="right">Wide</td>`, "<td>&lt;b&gt;raw&lt;/b&gt;</td>", `<span class="tg-spoiler">s</span>`,
		`<span class="rich-label">1.</span> <p>one</p>`, `<input type="checkbox" checked disabled>`,
		"<details><summary>Sum</summary><pre><code>x := 1</code></pre></details>",
		`<span class="media-tag">photo</span><figcaption>Pic</figcaption>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, "javascript") {
		t.Errorf("unsafe url rendered: %s", got)
	}
}

func TestDebugChatShowsRichMessage(t *testing.T) {
	const token = "741:rich"
	srv := New(Config{Quiet: true})
	doRichTestPOST(t, srv, "/bot"+token+"/sendRichMessage",
		`{"chat_id":42,"rich_message":{"html":"<table><tr><td>cell</td></tr></table>"}}`)
	doRichTestPOST(t, srv, "/bot"+token+"/sendRichMessage",
		`{"chat_id":42,"rich_message":{"markdown":"**md** <b>x</b>"}}`)
	// parse_mode=HTML still rejects tables.
	doRichTestPOST(t, srv, "/bot"+token+"/sendMessage",
		`{"chat_id":42,"parse_mode":"HTML","text":"<table><tr><td>plain</td></tr></table>"}`)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/chat/"+token+"/42/messages", nil))
	body := rec.Body.String()
	if strings.Count(body, "<table") != 1 || !strings.Contains(body, "<td>cell</td>") {
		t.Fatalf("only the rich message may render a table:\n%s", body)
	}
	if !strings.Contains(body, "**md** &lt;b&gt;x&lt;") {
		t.Fatalf("markdown must render as escaped text:\n%s", body)
	}
}
