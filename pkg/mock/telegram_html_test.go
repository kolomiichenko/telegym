package mock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenderTelegramHTML(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		notWant []string
	}{
		{
			name: "formatting tags and synonyms",
			in:   `<b>b</b><strong>s</strong><i>i</i><em>e</em><u>u</u><ins>n</ins><s>x</s><strike>k</strike><del>d</del>`,
			want: []string{"<b>b</b><b>s</b><i>i</i><i>e</i><u>u</u><u>n</u><s>x</s><s>k</s><s>d</s>"},
		},
		{
			name: "safe links",
			in:   `<a href="https://example.com/a?b=1&amp;c=2">web</a> <a href="tg://user?id=1">user</a>`,
			want: []string{
				`<a href="https://example.com/a?b=1&amp;c=2" target="_blank" rel="noopener noreferrer">web</a>`,
				`<a href="tg://user?id=1" target="_blank" rel="noopener noreferrer">user</a>`,
			},
		},
		{
			name:    "unsafe link keeps text only",
			in:      `<a href="javascript:alert(1)">x</a>`,
			want:    []string{"x"},
			notWant: []string{"<a", "javascript"},
		},
		{
			name:    "unsupported tags dropped, text kept",
			in:      `<table><tr><td>cell</td></tr></table><script>alert(1)</script><div onclick="x()">div</div><br>`,
			want:    []string{"cell", "div"},
			notWant: []string{"<table", "<td", "<script", "<div", "onclick", "<br>"},
		},
		{
			name: "spoilers, quotes, code",
			in: `<tg-spoiler>a</tg-spoiler><span class="tg-spoiler">b</span><span>c</span><blockquote>q</blockquote><blockquote expandable>e</blockquote><pre><code class="language-go">x := 1
y := 2</code></pre>`,
			want: []string{
				`<span class="tg-spoiler">a</span><span class="tg-spoiler">b</span>c`,
				`<blockquote>q</blockquote><blockquote class="expandable">e</blockquote>`,
				"<pre><code>x := 1\ny := 2</code></pre>",
			},
		},
		{
			name:    "newlines and escaping",
			in:      "line 1\nline 2 &lt;b&gt; 5 &gt; 3",
			want:    []string{"line 1<br>line 2 &lt;b&gt; 5 &gt; 3"},
			notWant: []string{"<b>"},
		},
		{
			name:    "commands only at start or after whitespace",
			in:      "/start, yes/no, see https://t.me/x\n/help",
			want:    []string{`hx-swap="none">/start</a>, yes/no, see https://t.me/x<br><a class="cmd"`, `hx-swap="none">/help</a>`},
			notWant: []string{`>/no</a>`, `>/x</a>`},
		},
		{
			name:    "commands clickable outside links and code",
			in:      `Try /start or <code>/help</code>`,
			want:    []string{`<a class="cmd"`, "<code>/help</code>"},
			notWant: []string{`hx-vals="{&#34;name&#34;:&#34;n&#34;,&#34;text&#34;:&#34;/help`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(renderTelegramHTML(tt.in, "tok", 1, "n"))
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in %q", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in %q", w, got)
				}
			}
		})
	}
}

func TestDebugChatRendersHTMLOnlyForParseModeHTML(t *testing.T) {
	const token = "741:html"
	srv := New(Config{Quiet: true})
	send := func(method, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/bot"+token+"/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", method, rec.Code, rec.Body.String())
		}
	}
	send("sendMessage", `{"chat_id":42,"text":"<b>bold html</b>","parse_mode":"HTML"}`)
	send("sendMessage", `{"chat_id":42,"text":"plain <b>x with /help"}`)
	send("sendMessage", `{"chat_id":42,"text":"*md* <i>not html","parse_mode":"MarkdownV2"}`)
	send("sendPhoto", `{"chat_id":42,"photo":"file-1","caption":"<i>caption html</i>","parse_mode":"HTML"}`)
	// Telegram keeps runs of spaces in text and HTML bodies; rich HTML collapses them.
	send("sendMessage", `{"chat_id":42,"text":"Column 1      Column 2\n 1   She sees me."}`)
	send("sendMessage", `{"chat_id":42,"text":"<b>N</b>      V","parse_mode":"HTML"}`)
	send("sendRichMessage", `{"chat_id":42,"rich_message":{"html":"<p>rich\n   text</p>"}}`)

	expect := func(path string, wants ...string) {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("missing %q in %s:\n%s", want, path, body)
			}
		}
	}
	expect("/debug/chat/"+token+"/42/messages",
		"<b>bold html</b>",
		"plain &lt;b&gt;x with",
		`<a class="cmd"`,
		"&lt;i&gt;not html",
		"<i>caption html</i>",
		`<div class="msg-text">Column 1      Column 2<br> 1   She sees me.</div>`,
		`<div class="msg-text"><b>N</b>      V</div>`,
		`<div class="msg-text"><div class="rich">`,
	)
	expect("/debug/chat/"+token+"/42",
		".msg-text { white-space: pre-wrap; }",
		".msg-text .rich { white-space: normal; }",
	)
}

func BenchmarkRenderTelegramHTML(b *testing.B) {
	const sample = "Lesson <b>one</b>: see <a href=\"https://example.com/lesson-1\">the hub</a>,\n" +
		"<i>gloss</i> and <code>code</code>. Next: /next or /menu."
	b.ReportAllocs()
	for b.Loop() {
		_ = renderTelegramHTML(sample, "tok", 1, "n")
	}
}
