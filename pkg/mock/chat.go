package mock

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"math/rand/v2"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// HTMX debug-chat UI: lets a human talk to a bot through the mock while
// load tests are running in parallel. Self-contained - templates and the
// htmx.min.js runtime are embedded in the binary, no CDN dependency.

//go:embed web/templates/*.html
var chatTemplatesFS embed.FS

// htmx and idiomorph are vendored as embedded assets (web/htmx.min.js,
// web/idiomorph-ext.min.js) so the debug chat works fully offline and
// the binary is self-contained. Refresh with `make refresh-web-deps`.
// Both libraries are 0BSD-licensed (no attribution required).

//go:embed web/htmx.min.js
var chatStaticHTMX []byte

//go:embed web/idiomorph-ext.min.js
var chatStaticIdiomorph []byte

var chatTemplates = template.Must(
	template.New("").Funcs(template.FuncMap{
		"toJSON": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"eq": func(a, b string) bool { return a == b },
	}).ParseFS(chatTemplatesFS, "web/templates/*.html"),
)

// chatHandlers holds dependencies the UI needs (store for messages,
// dispatcher for delivering injected updates to the bot's webhook).
type chatHandlers struct {
	store      *Store
	dispatcher *WebhookDispatcher
}

func newChatHandlers(s *Store, d *WebhookDispatcher) *chatHandlers {
	return &chatHandlers{store: s, dispatcher: d}
}

// register wires the chat routes onto the given router group.
func (h *chatHandlers) register(r *gin.RouterGroup, defaultToken string) {
	r.GET("", func(c *gin.Context) { h.home(c, defaultToken) })
	r.GET("/open", h.open)
	r.GET("/static/htmx.min.js", h.serveHTMX)
	r.GET("/static/idiomorph-ext.min.js", h.serveIdiomorph)
	r.GET("/:token/:chat_id", h.view)
	r.GET("/:token/:chat_id/messages", h.messages)
	r.POST("/:token/:chat_id/send", h.send)
	r.POST("/:token/:chat_id/click", h.click)
}

type homeVM struct {
	DefaultToken  string
	DefaultChatID int64
	DefaultName   string
	Recent        []recentChatVM
	Bots          []BotInfo
}

// Randomized defaults so two browser tabs on the same machine don't
// trample each other's chat_id, and a fresh tester gets a memorable
// display name without having to type one.
var (
	nameAdjectives = []string{
		"wild", "calm", "swift", "brave", "clever", "happy",
		"bright", "quiet", "noble", "lucky", "sleepy", "sharp",
	}
	nameAnimals = []string{
		"otter", "falcon", "panda", "fox", "badger", "owl",
		"lynx", "hare", "deer", "wolf", "raven", "puma",
	}
)

func randomName() string {
	return nameAdjectives[rand.IntN(len(nameAdjectives))] +
		"_" + nameAnimals[rand.IntN(len(nameAnimals))]
}

// 10-digit chat_id mirrors how modern Telegram allocates user_ids
// (current real users are ~1.7e9). Roomy enough to never collide with
// manual ad-hoc IDs (999001-style) used in earlier sessions and well
// below k6's UnixMilli-based VU IDs (~1.7e12). 9 billion possibilities
// - practically zero risk of two tabs picking the same one.
func randomChatID() int64 {
	return 1_000_000_000 + rand.Int64N(8_999_999_999)
}

type recentChatVM struct {
	Token    string
	ChatID   int64
	Username string
	LastSeen string
}

func (h *chatHandlers) home(c *gin.Context, defaultToken string) {
	vm := homeVM{
		DefaultToken:  defaultToken,
		DefaultChatID: randomChatID(),
		DefaultName:   randomName(),
		// Recent intentionally empty server-side - rendered client-side
		// from localStorage by the home template's <script>.
		Bots: h.store.Bots(),
	}
	render(c, "home.html", vm)
}

// open turns a form GET into a clean URL: /debug/chat/<token>/<chat_id>?name=...
func (h *chatHandlers) open(c *gin.Context) {
	token := c.Query("token")
	chatID := c.Query("chat_id")
	name := c.DefaultQuery("name", "andrii")
	if token == "" || chatID == "" {
		c.Redirect(http.StatusFound, "/debug/chat")
		return
	}
	c.Redirect(http.StatusFound, fmt.Sprintf("/debug/chat/%s/%s?name=%s", token, chatID, name))
}

type chatVM struct {
	Token      string
	TokenShort string
	ChatID     int64
	Name       string
	WebhookSet bool
}

func (h *chatHandlers) view(c *gin.Context) {
	token := c.Param("token")
	chatID, err := strconv.ParseInt(c.Param("chat_id"), 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "bad chat_id")
		return
	}
	bot := h.store.Bot(token)
	webhookURL, _ := bot.Webhook()

	vm := chatVM{
		Token:      token,
		TokenShort: shortenToken(token),
		ChatID:     chatID,
		Name:       c.DefaultQuery("name", "andrii"),
		WebhookSet: webhookURL != "",
	}
	render(c, "chat.html", vm)
}

type msgVM struct {
	Side          string
	Text          template.HTML // pre-rendered: HTML-escaped, /commands wrapped clickable
	MessageID     int
	Stamp         string
	ButtonRows    [][]btnVM // preserves the bot's 2D inline-keyboard layout
	MediaTag      string
	StickerEmoji  string // empty if bot did not supply an emoji
	StickerFileID string // short prefix shown when emoji is missing (debugging aid)
}

type btnVM struct {
	Text         string
	CallbackData string
	MessageID    int
	Style        string // "primary" | "success" | "danger" | "" (default)
}

type messagesVM struct {
	Items         []msgVM
	Token         string
	ChatID        int64
	Name          string
	EmptyHint     template.HTML // pre-rendered hint for the empty state ("try /start ...")
	ReplyKeyboard [][]string    // current reply keyboard rows (button texts); nil if cleared
	Placeholder   string        // input_field_placeholder from latest reply keyboard
	Toast         *toastVM      // pending callback toast for this chat, drained on read
}

type toastVM struct {
	Text      string
	ShowAlert bool
}

// messages renders the conversation list - bot outbound + user inputs,
// merged and sorted chronologically. Called every 1s via HTMX polling.
// Also computes the current reply-keyboard state and emits it as an
// out-of-band swap so the chat input area updates on the same poll.
func (h *chatHandlers) messages(c *gin.Context) {
	token := c.Param("token")
	chatID, _ := strconv.ParseInt(c.Param("chat_id"), 10, 64)
	name := c.DefaultQuery("name", "andrii")
	bot := h.store.Bot(token)

	botMsgs := bot.Messages(chatID, 200)
	items := mergeChatLog(botMsgs, bot.Inputs(chatID), token, chatID, name)
	emptyHint := renderTextWithCommands(
		"empty chat - start with /start (or any /command in text)",
		token, chatID, name,
	)
	kbRows, placeholder := currentReplyKeyboard(botMsgs)

	// Drain at most one pending callback toast per poll. The OOB <div id="toast">
	// only swaps when a fresh toast is present, otherwise the existing in-flight
	// toast (if any) keeps fading naturally.
	var toast *toastVM
	if t, ok := bot.PopToast(chatID); ok {
		toast = &toastVM{Text: t.Text, ShowAlert: t.ShowAlert}
	}

	render(c, "messages.html", messagesVM{
		Items:         items,
		Token:         token,
		ChatID:        chatID,
		Name:          name,
		EmptyHint:     emptyHint,
		ReplyKeyboard: kbRows,
		Placeholder:   placeholder,
		Toast:         toast,
	})
}

// currentReplyKeyboard derives the chat-input keyboard from message
// history: walks backwards through bot messages, returns the first
// reply keyboard or remove_keyboard encountered. Inline keyboards are
// ignored - they live in message bubbles, not at the chat level.
func currentReplyKeyboard(msgs []Message) (rows [][]string, placeholder string) {
	// Messages from bot.Messages() are newest-first.
	for _, m := range msgs {
		rm := m.ReplyMarkup
		if rm == nil {
			continue
		}
		switch {
		case rm.RemoveKeyboard:
			return nil, "" // explicitly cleared by bot
		case len(rm.Keyboard) > 0:
			out := make([][]string, 0, len(rm.Keyboard))
			for _, row := range rm.Keyboard {
				texts := make([]string, 0, len(row))
				for _, b := range row {
					texts = append(texts, b.Text)
				}
				out = append(out, texts)
			}
			return out, rm.InputFieldPlaceholder
		}
	}
	return nil, ""
}

// send injects the user's typed message as an Update.
func (h *chatHandlers) send(c *gin.Context) {
	token := c.Param("token")
	chatID, _ := strconv.ParseInt(c.Param("chat_id"), 10, 64)
	text := c.PostForm("text")
	name := c.DefaultPostForm("name", "andrii")
	if text == "" {
		c.Status(http.StatusOK)
		return
	}
	bot := h.store.Bot(token)
	bot.AppendInput(UserInput{ChatID: chatID, Text: text, Date: time.Now().Unix(), Name: name})

	upd := Update{
		UpdateID: h.store.NextUpdateID(),
		Message: &Message{
			MessageID: h.store.NextMessageID(),
			From:      &User{ID: chatID, FirstName: name, Username: name},
			Date:      time.Now().Unix(),
			Chat:      Chat{ID: chatID, Type: "private", FirstName: name, Username: name},
			Text:      text,
		},
	}
	if _, err := h.dispatcher.Deliver(c.Request.Context(), bot, upd); err != nil {
		c.String(http.StatusBadGateway, "deliver: %v", err)
		return
	}
	c.Status(http.StatusNoContent)
}

// click injects a callback query for the button the user clicked.
// HTMX's hx-vals attribute sends form-urlencoded by default, so the request
// binding has to accept both form and json (curl/api callers).
type clickReq struct {
	Data      string `json:"data"       form:"data"       binding:"required"`
	MessageID int    `json:"message_id" form:"message_id"`
	Name      string `json:"name"       form:"name"`
}

func (h *chatHandlers) click(c *gin.Context) {
	token := c.Param("token")
	chatID, _ := strconv.ParseInt(c.Param("chat_id"), 10, 64)

	var req clickReq
	if err := c.ShouldBind(&req); err != nil {
		c.String(http.StatusBadRequest, "bad request: %v", err)
		return
	}
	if req.Name == "" {
		req.Name = "andrii"
	}
	bot := h.store.Bot(token)
	// Inline-button clicks are NOT recorded as user messages - real Telegram
	// clients don't show them in chat history; they just flash the button to
	// signal the tap was accepted. The chat template's CSS handles the
	// transient visual feedback.

	from := User{ID: chatID, FirstName: req.Name, Username: req.Name}
	cbID := strconv.FormatInt(time.Now().UnixNano(), 10)
	bot.RegisterCallback(cbID, chatID)
	upd := Update{
		UpdateID: h.store.NextUpdateID(),
		CallbackQuery: &CallbackQuery{
			ID:           cbID,
			From:         from,
			ChatInstance: strconv.FormatInt(chatID, 10),
			Data:         req.Data,
			Message: &Message{
				MessageID: req.MessageID,
				Chat:      Chat{ID: chatID, Type: "private"},
				Date:      time.Now().Unix(),
			},
		},
	}
	if _, err := h.dispatcher.Deliver(c.Request.Context(), bot, upd); err != nil {
		c.String(http.StatusBadGateway, "deliver: %v", err)
		return
	}
	c.Status(http.StatusNoContent)
}

// mergeChatLog combines bot outbound and user inputs into a single
// time-ordered view suitable for the chat template. Renders /commands in
// any text as clickable spans so the developer can re-fire them with one
// click - same affordance Telegram clients offer.
//
// Ordering uses the monotonic Seq shared by Message.MessageID and
// UserInput.Seq (both allocated from Store.messageIDSeed).
//
// Telegram-style edit dedup: the store keeps edits as new append entries
// (so the xk6 poller can see edits as events with bumped sequence). Here
// we collapse them to a single visual entry per (chat_id, message_id) --
// the LATEST one in chronological order wins, matching how a real
// Telegram client replaces the bubble in place when a message is edited.
func mergeChatLog(botMsgs []Message, userInputs []UserInput, token string, chatID int64, name string) []msgVM {
	type entry struct {
		seq int
		vm  msgVM
	}
	all := make([]entry, 0, len(botMsgs)+len(userInputs))

	// botMsgs comes newest-first from the store, so the FIRST occurrence
	// per message_id is the latest edit. Skip subsequent (older) entries
	// with the same id - they're stale pre-edit versions kept for the
	// xk6 poller's benefit but not relevant to the chat view.
	seenID := map[int]bool{}

	for _, m := range botMsgs {
		if seenID[m.MessageID] {
			continue
		}
		seenID[m.MessageID] = true
		v := msgVM{
			Side:      "bot",
			Text:      renderTextWithCommands(pickText(m), token, chatID, name),
			MessageID: m.MessageID,
			Stamp:     time.Unix(m.Date, 0).Format("15:04:05"),
			MediaTag:  mediaTag(m),
		}
		if m.Sticker != nil {
			// Only show emoji if the bot actually supplied one. Otherwise
			// render the truncated file_id - that's the most honest signal:
			// "a sticker was sent, here's its identifier, no emoji metadata
			// available from the mock". Faking an emoji (random or hashed)
			// confuses real-vs-placeholder visually.
			v.StickerEmoji = m.Sticker.Emoji
			v.StickerFileID = m.Sticker.FileID
			if n := len(v.StickerFileID); n > 14 {
				v.StickerFileID = v.StickerFileID[:8] + "…" + v.StickerFileID[n-4:]
			}
		}
		if m.ReplyMarkup != nil {
			// Preserve the bot's 2D layout - collapsing rows looked wrong
			// for keyboards that intentionally stack two-per-row vs three-
			// per-row, etc.
			for _, row := range m.ReplyMarkup.InlineKeyboard {
				rowVM := make([]btnVM, 0, len(row))
				for _, b := range row {
					if b.CallbackData == "" {
						continue
					}
					rowVM = append(rowVM, btnVM{
						Text: b.Text, CallbackData: b.CallbackData, MessageID: m.MessageID,
						Style: b.Style,
					})
				}
				if len(rowVM) > 0 {
					v.ButtonRows = append(v.ButtonRows, rowVM)
				}
			}
		}
		all = append(all, entry{seq: m.MessageID, vm: v})
	}
	for _, in := range userInputs {
		all = append(all, entry{
			seq: in.Seq,
			vm: msgVM{
				Side:  "user",
				Text:  renderTextWithCommands(in.Text, token, chatID, name),
				Stamp: time.Unix(in.Date, 0).Format("15:04:05"),
			},
		})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].seq < all[j].seq })

	out := make([]msgVM, len(all))
	for i, e := range all {
		out[i] = e.vm
	}
	return out
}

// commandRe matches Telegram-style bot commands: /word possibly followed by
// alphanumerics and underscores. Conservative so it doesn't grab URLs like
// `https://x/y` or paths inside messages.
var commandRe = regexp.MustCompile(`/[a-zA-Z][a-zA-Z0-9_]*`)

// renderTextWithCommands HTML-escapes text, then wraps every /command match
// in an HTMX-driven clickable element that POSTs to /send via the same
// path as the compose form. Newlines become <br> so multi-line bot replies
// render with their original structure.
//
// Caller passes (token, chatID, name) so the rendered button targets the
// correct chat without any per-element template lookup.
func renderTextWithCommands(text, token string, chatID int64, name string) template.HTML {
	if text == "" {
		return ""
	}
	escaped := template.HTMLEscapeString(text)

	out := commandRe.ReplaceAllStringFunc(escaped, func(cmd string) string {
		// cmd is matched against already-escaped text; it can only contain
		// safe characters (/[A-Za-z0-9_]+) so no further escaping needed.
		vals, _ := json.Marshal(map[string]string{"text": cmd, "name": name})
		return fmt.Sprintf(
			`<a class="cmd" hx-post="/debug/chat/%s/%d/send" hx-vals="%s" hx-swap="none">%s</a>`,
			template.HTMLEscapeString(token), chatID,
			template.HTMLEscapeString(string(vals)), cmd,
		)
	})
	out = strings.ReplaceAll(out, "\n", "<br>")
	return template.HTML(out)
}

func pickText(m Message) string {
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}

func mediaTag(m Message) string {
	switch {
	case m.Photo != nil:
		return "photo"
	case m.Video != nil:
		return "video"
	case m.Animation != nil:
		return "animation"
	case m.Sticker != nil:
		return "sticker"
	case m.Dice != nil:
		return "dice"
	}
	return ""
}

func shortenToken(t string) string {
	if len(t) <= 12 {
		return t
	}
	return t[:6] + "…" + t[len(t)-4:]
}

func (h *chatHandlers) serveHTMX(c *gin.Context) {
	c.Data(http.StatusOK, "application/javascript", chatStaticHTMX)
}

func (h *chatHandlers) serveIdiomorph(c *gin.Context) {
	c.Data(http.StatusOK, "application/javascript", chatStaticIdiomorph)
}

func render(c *gin.Context, name string, data any) {
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := chatTemplates.ExecuteTemplate(c.Writer, name, data); err != nil {
		c.String(http.StatusInternalServerError, "template %s: %v", name, err)
	}
}
