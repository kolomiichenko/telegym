package mock

import (
	"encoding/json"
	"html"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// RichMessage is Message.rich_message. Blocks are kept as the JSON the
// bot sent; the mock does not parse html/markdown input into blocks, so
// messages sent that way come back with an empty block list.
type RichMessage struct {
	Blocks []json.RawMessage `json:"blocks"`
	IsRTL  bool              `json:"is_rtl,omitempty"`

	// Source the bot sent in InputRichMessage.html / .markdown (mock internal).
	HTML     string `json:"-"`
	Markdown string `json:"-"`
}

// SendRichMessage stores a rich message (Bot API 10.3) so the debug chat
// can render its html or blocks. Media uploads are not captured.
func (h *Handlers) SendRichMessage(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	chatID := toInt64(body["chat_id"])
	if chatID == 0 {
		errResp(c, http.StatusBadRequest, "Bad Request: chat_id is required")
		return
	}
	var in struct {
		Blocks   []json.RawMessage `json:"blocks"`
		HTML     string            `json:"html"`
		Markdown string            `json:"markdown"`
		IsRTL    bool              `json:"is_rtl"`
	}
	raw := body["rich_message"]
	enc, err := rawJSON(raw)
	if raw == nil || err != nil || json.Unmarshal(enc, &in) != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: rich_message is required")
		return
	}
	set := 0
	for _, used := range []bool{len(in.Blocks) > 0, in.HTML != "", in.Markdown != ""} {
		if used {
			set++
		}
	}
	if set != 1 {
		errResp(c, http.StatusBadRequest, "Bad Request: exactly one of rich_message.html, rich_message.markdown or rich_message.blocks must be specified")
		return
	}

	var reply struct {
		MessageID int `json:"message_id"`
	}
	enc, err = rawJSON(body["reply_parameters"])
	if err != nil || json.Unmarshal(enc, &reply) != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: invalid reply_parameters")
		return
	}

	m := h.newBotMessage(bot, chatID)
	m.ReplyMarkup = parseReplyMarkup(body["reply_markup"])
	setMessageThread(&m, body)
	if reply.MessageID != 0 {
		m.ReplyToMessage = &Message{MessageID: reply.MessageID, Chat: m.Chat, Date: m.Date}
	}
	blocks := in.Blocks
	if blocks == nil {
		blocks = []json.RawMessage{}
	}
	m.RichMessage = &RichMessage{Blocks: blocks, IsRTL: in.IsRTL, HTML: in.HTML, Markdown: in.Markdown}
	bot.AppendMessage(m)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: m})
}

// renderRichMessage renders a rich message body for the debug chat.
// Markdown is shown as plain text: the mock has no Markdown parser.
func renderRichMessage(rm *RichMessage, token string, chatID int64, name string) template.HTML {
	switch {
	case rm.HTML != "":
		nodes, err := nethtml.ParseFragment(strings.NewReader(rm.HTML), &nethtml.Node{
			Type: nethtml.ElementNode, Data: "body", DataAtom: atom.Body,
		})
		if err != nil {
			return renderTextWithCommands(rm.HTML, token, chatID, name)
		}
		r := telegramHTMLRenderer{token: token, chatID: chatID, name: name, rich: true}
		for _, n := range nodes {
			r.render(n, htmlContext{})
		}
		return template.HTML(`<div class="rich">` + r.b.String() + `</div>`)
	case rm.Markdown != "":
		return renderTextWithCommands(rm.Markdown, token, chatID, name)
	}
	var b strings.Builder
	b.WriteString(`<div class="rich">`)
	for _, raw := range rm.Blocks {
		var block map[string]any
		if json.Unmarshal(raw, &block) == nil {
			writeRichBlock(&b, block)
		}
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

// richHTMLSimpleTags are rich HTML tags emitted as-is, without attributes.
var richHTMLSimpleTags = map[atom.Atom]bool{
	atom.Mark: true, atom.Sub: true, atom.Sup: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.P: true, atom.Footer: true, atom.Ul: true, atom.Aside: true, atom.Cite: true,
	atom.Figure: true, atom.Figcaption: true, atom.Caption: true, atom.Summary: true,
	atom.Thead: true, atom.Tbody: true, atom.Tfoot: true, atom.Tr: true,
}

// richMediaTags map media elements to the placeholder the debug chat shows.
var richMediaTags = map[string]string{
	"img": "photo", "video": "video", "audio": "audio", "tg-document": "document", "tg-map": "map",
}

// renderRichElement handles the tags sendRichMessage html supports on top
// of parse_mode=HTML (see https://core.telegram.org/bots/api#rich-html-style).
// It reports false for tags the plain HTML renderer handles.
func (r *telegramHTMLRenderer) renderRichElement(n *nethtml.Node, ctx htmlContext) bool {
	wrap := func(open, closeTag string) bool {
		r.b.WriteString(open)
		r.renderChildren(n, ctx)
		r.b.WriteString(closeTag)
		return true
	}
	switch {
	case richHTMLSimpleTags[n.DataAtom]:
		return wrap("<"+n.Data+">", "</"+n.Data+">")
	case n.DataAtom == atom.Br:
		r.b.WriteString("<br>")
		return true
	case n.DataAtom == atom.Hr:
		r.b.WriteString("<hr>")
		return true
	case n.DataAtom == atom.Ol:
		attrs := ""
		if v := attrValue(n, "start"); isUint(v) {
			attrs += ` start="` + v + `"`
		}
		if v := attrValue(n, "type"); v != "" && strings.Contains("aAiI1", v) && len(v) == 1 {
			attrs += ` type="` + v + `"`
		}
		if hasAttr(n, "reversed") {
			attrs += " reversed"
		}
		return wrap("<ol"+attrs+">", "</ol>")
	case n.DataAtom == atom.Li:
		if v := attrValue(n, "value"); isUint(v) {
			return wrap(`<li value="`+v+`">`, "</li>")
		}
		return wrap("<li>", "</li>")
	case n.DataAtom == atom.Input:
		if attrValue(n, "type") == "checkbox" {
			r.b.WriteString(richCheckbox(hasAttr(n, "checked")))
		}
		return true
	case n.DataAtom == atom.Table:
		return wrap(`<div class="rich-table-wrap"><table class="`+
			richTableClass(hasAttr(n, "bordered"), hasAttr(n, "striped"), hasAttr(n, "compact"))+`">`, "</table></div>")
	case n.DataAtom == atom.Th || n.DataAtom == atom.Td:
		return wrap("<"+n.Data+richCellAttrs(attrValue(n, "colspan"), attrValue(n, "rowspan"),
			attrValue(n, "align"), attrValue(n, "valign"))+">", "</"+n.Data+">")
	case n.DataAtom == atom.Details:
		if hasAttr(n, "open") {
			return wrap("<details open>", "</details>")
		}
		return wrap("<details>", "</details>")
	case richMediaTags[n.Data] != "":
		if n.DataAtom == atom.Img && strings.HasPrefix(attrValue(n, "src"), "tg://emoji") {
			r.b.WriteString(html.EscapeString(attrValue(n, "alt")))
			return true
		}
		r.b.WriteString(`<span class="media-tag">` + richMediaTags[n.Data] + `</span>`)
		return true
	case n.Data == "tg-math":
		ctx.literal = true
		return wrap("<code>", "</code>")
	case n.Data == "tg-math-block":
		ctx.literal, ctx.pre = true, true
		return wrap("<pre>", "</pre>")
	case n.Data == "tg-button":
		return wrap(`<span class="rich-button">`, "</span>")
	case n.Data == "tg-button-row":
		return wrap(`<div class="rich-button-row">`, "</div>")
	case n.DataAtom == atom.A && strings.HasPrefix(attrValue(n, "href"), "#"):
		ctx.literal = true
		return wrap(`<a href="`+html.EscapeString(attrValue(n, "href"))+`">`, "</a>")
	}
	return false
}

func richTableClass(bordered, striped, compact bool) string {
	class := "rich-table"
	if bordered {
		class += " bordered"
	}
	if striped {
		class += " striped"
	}
	if compact {
		class += " compact"
	}
	return class
}

func richCellAttrs(colspan, rowspan, align, valign string) string {
	attrs := ""
	if n, err := strconv.Atoi(colspan); err == nil && n > 1 && n <= 20 {
		attrs += ` colspan="` + colspan + `"`
	}
	if n, err := strconv.Atoi(rowspan); err == nil && n > 1 && n <= 500 {
		attrs += ` rowspan="` + rowspan + `"`
	}
	switch align {
	case "left", "center", "right":
		attrs += ` align="` + align + `"`
	}
	switch valign {
	case "top", "middle", "bottom":
		attrs += ` valign="` + valign + `"`
	}
	return attrs
}

func richCheckbox(checked bool) string {
	if checked {
		return `<input type="checkbox" checked disabled> `
	}
	return `<input type="checkbox" disabled> `
}

func isUint(s string) bool {
	if s == "" || len(s) > 6 {
		return false
	}
	_, err := strconv.ParseUint(s, 10, 32)
	return err == nil
}

// writeRichBlock renders one RichBlock / InputRichBlock. Media and other
// block types the debug chat cannot show become a placeholder tag.
func writeRichBlock(b *strings.Builder, block map[string]any) {
	text := func(key string) string { return richTextHTML(block[key]) }
	blocks := func(key string) {
		items, _ := block[key].([]any)
		for _, item := range items {
			if child, ok := item.(map[string]any); ok {
				writeRichBlock(b, child)
			}
		}
	}
	credit := func() {
		if block["credit"] != nil {
			b.WriteString("<cite>" + text("credit") + "</cite>")
		}
	}
	caption := func() {
		if c, ok := block["caption"].(map[string]any); ok {
			b.WriteString("<figcaption>" + richTextHTML(c["text"]))
			if c["credit"] != nil {
				b.WriteString("<cite>" + richTextHTML(c["credit"]) + "</cite>")
			}
			b.WriteString("</figcaption>")
		}
	}
	typ := toString(block["type"])
	switch typ {
	case "paragraph":
		b.WriteString("<p>" + text("text") + "</p>")
	case "heading":
		size := min(max(toInt(block["size"]), 1), 6)
		tag := "h" + strconv.Itoa(size)
		b.WriteString("<" + tag + ">" + text("text") + "</" + tag + ">")
	case "pre":
		b.WriteString("<pre><code>" + text("text") + "</code></pre>")
	case "footer":
		b.WriteString("<footer>" + text("text") + "</footer>")
	case "divider":
		b.WriteString("<hr>")
	case "mathematical_expression":
		b.WriteString("<pre>" + html.EscapeString(toString(block["expression"])) + "</pre>")
	case "anchor":
	case "list":
		b.WriteString(`<ul class="rich-list">`)
		items, _ := block["items"].([]any)
		for _, item := range items {
			li, ok := item.(map[string]any)
			if !ok {
				continue
			}
			b.WriteString("<li>")
			if v, _ := li["has_checkbox"].(bool); v {
				checked, _ := li["is_checked"].(bool)
				b.WriteString(richCheckbox(checked))
			} else if label := toString(li["label"]); label != "" {
				b.WriteString(`<span class="rich-label">` + html.EscapeString(label) + "</span> ")
			}
			children, _ := li["blocks"].([]any)
			for _, child := range children {
				if cb, ok := child.(map[string]any); ok {
					writeRichBlock(b, cb)
				}
			}
			b.WriteString("</li>")
		}
		b.WriteString("</ul>")
	case "blockquote":
		b.WriteString("<blockquote>")
		blocks("blocks")
		credit()
		b.WriteString("</blockquote>")
	case "expandable_blockquote":
		b.WriteString(`<blockquote class="expandable">` + text("text"))
		credit()
		b.WriteString("</blockquote>")
	case "pullquote":
		b.WriteString("<aside>" + text("text"))
		credit()
		b.WriteString("</aside>")
	case "table":
		bordered, _ := block["is_bordered"].(bool)
		striped, _ := block["is_striped"].(bool)
		compact, _ := block["is_compact"].(bool)
		b.WriteString(`<div class="rich-table-wrap"><table class="` + richTableClass(bordered, striped, compact) + `">`)
		if block["caption"] != nil {
			b.WriteString("<caption>" + text("caption") + "</caption>")
		}
		rows, _ := block["cells"].([]any)
		for _, row := range rows {
			b.WriteString("<tr>")
			cells, _ := row.([]any)
			for _, c := range cells {
				cell, _ := c.(map[string]any)
				tag := "td"
				if v, _ := cell["is_header"].(bool); v {
					tag = "th"
				}
				b.WriteString("<" + tag + richCellAttrs(
					strconv.Itoa(toInt(cell["colspan"])), strconv.Itoa(toInt(cell["rowspan"])),
					toString(cell["align"]), toString(cell["valign"]),
				) + ">" + richTextHTML(cell["text"]) + "</" + tag + ">")
			}
			b.WriteString("</tr>")
		}
		b.WriteString("</table></div>")
	case "details":
		if open, _ := block["is_open"].(bool); open {
			b.WriteString("<details open>")
		} else {
			b.WriteString("<details>")
		}
		b.WriteString("<summary>" + text("summary") + "</summary>")
		blocks("blocks")
		b.WriteString("</details>")
	case "collage", "slideshow":
		b.WriteString("<figure>")
		blocks("blocks")
		caption()
		b.WriteString("</figure>")
	default:
		b.WriteString(`<span class="media-tag">` + html.EscapeString(strings.ReplaceAll(typ, "_", " ")) + "</span>")
		caption()
	}
}

// richTextTags maps RichText types to the HTML element that wraps them.
var richTextTags = map[string]string{
	"bold": "b", "italic": "i", "underline": "u", "strikethrough": "s",
	"code": "code", "marked": "mark", "subscript": "sub", "superscript": "sup",
}

// richTextHTML renders a RichText: a string, an array, or a typed object.
func richTextHTML(v any) string {
	switch t := v.(type) {
	case string:
		return html.EscapeString(t)
	case []any:
		var b strings.Builder
		for _, part := range t {
			b.WriteString(richTextHTML(part))
		}
		return b.String()
	case map[string]any:
		typ := toString(t["type"])
		inner := richTextHTML(t["text"])
		if tag := richTextTags[typ]; tag != "" {
			return "<" + tag + ">" + inner + "</" + tag + ">"
		}
		switch typ {
		case "spoiler":
			return `<span class="tg-spoiler">` + inner + "</span>"
		case "url":
			if href := toString(t["url"]); isSafeRichURL(href) {
				return `<a href="` + html.EscapeString(href) + `" target="_blank" rel="noopener noreferrer">` + inner + "</a>"
			}
		case "email_address":
			return `<a href="mailto:` + html.EscapeString(toString(t["email_address"])) + `">` + inner + "</a>"
		case "custom_emoji":
			return html.EscapeString(toString(t["alternative_text"]))
		case "mathematical_expression":
			return "<code>" + html.EscapeString(toString(t["expression"])) + "</code>"
		case "button":
			if btn, ok := t["button"].(map[string]any); ok {
				return `<span class="rich-button">` + richTextHTML(btn["text"]) + "</span>"
			}
		}
		return inner
	}
	return ""
}

func isSafeRichURL(href string) bool {
	lower := strings.ToLower(href)
	for _, p := range []string{"http://", "https://", "tg://", "mailto:", "tel:"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}
