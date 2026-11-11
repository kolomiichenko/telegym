package mock

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// Widget limits from Bot API 10.4.
const (
	maxWidgetHTML           = 32768
	maxWidgetHTMLCompressed = 4096
	maxWidgetData           = 1024
	maxWidgetCaption        = 1024 // characters
	maxWidgetQueryData      = 64
	widgetQueryTTL          = 15 * time.Second
	// sharedWidgetEditInterval is how often a shared widget's public data
	// may change.
	sharedWidgetEditInterval = time.Second
	// maxWidgetQueries bounds unanswered queries kept per bot under load.
	maxWidgetQueries = 5000
	// Each user can send at most widgetQueryBurst queries per
	// widgetQueryWindow to the bot from all its widgets.
	widgetQueryBurst  = 5
	widgetQueryWindow = 5 * time.Second
)

// errWidgetQueryFlood refuses a widget query over the per-user limit.
var errWidgetQueryFlood = errors.New("user sent more than 5 widget queries in 5 seconds")

// errQueryInvalid is Telegram's answer to an unknown, expired or already
// answered query id.
const errQueryInvalid = "Bad Request: query is too old and response timeout expired or query ID is invalid"

// Widget is Message.widget (Bot API 10.4).
type Widget struct {
	ID         string `json:"id,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	HTML       string `json:"html"`
	PublicData string `json:"public_data,omitempty"`
	ChatData   string `json:"chat_data,omitempty"`

	// AllowQueries is the allow_queries flag the widget was sent with
	// (mock internal): injected queries require it.
	AllowQueries bool `json:"-"`
}

// WidgetQueryType is the type of a widget query. The mock injects only
// the "data" type; sign_data and sent_transaction need a TON wallet.
type WidgetQueryType struct {
	Type string `json:"type"`
}

// WidgetQuery is Update.widget_query: a widget in a message asked the bot.
type WidgetQuery struct {
	ID           string          `json:"id"`
	From         User            `json:"from"`
	Chat         *Chat           `json:"chat,omitempty"`
	MessageID    int             `json:"message_id,omitempty"`
	ChatInstance string          `json:"chat_instance"`
	Type         WidgetQueryType `json:"type"`
	Data         string          `json:"data"`
	WidgetID     string          `json:"widget_id,omitempty"`
}

// SharedWidgetQuery is Update.shared_widget_query: a shared widget asked
// the bot outside of a message.
type SharedWidgetQuery struct {
	ID       string          `json:"id"`
	From     User            `json:"from"`
	Type     WidgetQueryType `json:"type"`
	Data     string          `json:"data"`
	WidgetID string          `json:"widget_id"`
}

// widgetRegistry holds a bot's shared widgets and unanswered widget queries.
type widgetRegistry struct {
	mu      sync.Mutex
	shared  map[string]*sharedWidget
	queries map[string]time.Time  // widget_query_id → issued at
	sent    map[int64][]time.Time // user id → recent query times
}

type sharedWidget struct {
	widget   Widget // template for sendSharedWidget; ChatData unused
	lastEdit time.Time
}

func (r *widgetRegistry) addShared(id string, w Widget) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.shared == nil {
		r.shared = map[string]*sharedWidget{}
	}
	r.shared[id] = &sharedWidget{widget: w}
}

func (r *widgetRegistry) lookupShared(id string) (Widget, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sw, ok := r.shared[id]
	if !ok {
		return Widget{}, false
	}
	return sw.widget, true
}

// setPublicData changes a shared widget's public data, at most once per
// sharedWidgetEditInterval. It reports a missing widget, or how long to
// wait before the next edit.
func (r *widgetRegistry) setPublicData(id, data string, now time.Time) (wait time.Duration, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sw, ok := r.shared[id]
	if !ok {
		return 0, false
	}
	if next := sw.lastEdit.Add(sharedWidgetEditInterval); now.Before(next) {
		return next.Sub(now), true
	}
	sw.lastEdit = now
	sw.widget.PublicData = data
	return 0, true
}

// registerQuery records an injected query id so answerWidgetQuery can
// check it. Expired queries are dropped once the map is full.
func (r *widgetRegistry) registerQuery(id string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.queries == nil {
		r.queries = map[string]time.Time{}
	}
	if len(r.queries) >= maxWidgetQueries {
		for k, at := range r.queries {
			if now.Sub(at) > widgetQueryTTL {
				delete(r.queries, k)
			}
		}
	}
	r.queries[id] = now
}

// allowQuery counts a query from userID, refusing it once the user hit
// the per-user limit. Users idle for a full window are dropped once the
// map is full.
func (r *widgetRegistry) allowQuery(userID int64, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent == nil {
		r.sent = map[int64][]time.Time{}
	}
	if len(r.sent) >= maxWidgetQueries {
		for id, times := range r.sent {
			if now.Sub(times[len(times)-1]) >= widgetQueryWindow {
				delete(r.sent, id)
			}
		}
	}
	recent := r.sent[userID][:0:0]
	for _, at := range r.sent[userID] {
		if now.Sub(at) < widgetQueryWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) >= widgetQueryBurst {
		r.sent[userID] = recent
		return false
	}
	r.sent[userID] = append(recent, now)
	return true
}

// answerQuery consumes a query id; false means unknown, expired or answered.
func (r *widgetRegistry) answerQuery(id string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.queries[id]
	if !ok {
		return false
	}
	delete(r.queries, id)
	return now.Sub(at) <= widgetQueryTTL
}

// latestWidgetMessage returns the newest stored revision of a bot message
// that carries a widget.
func (b *botEntry) latestWidgetMessage(chatID int64, messageID int) (Message, bool) {
	l := b.existingChat(chatID)
	if l == nil {
		return Message{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	stored := newest(l.msgs, b.maxMessages)
	for i := len(stored) - 1; i >= 0; i-- {
		if m := stored[i]; m.MessageID == messageID && m.Widget != nil {
			return m, true
		}
	}
	return Message{}, false
}

// setSharedPublicData updates public_data in place on every stored message
// with the shared widget: Telegram changes it everywhere without an update.
// Stored widgets are replaced, never mutated, as readers may hold them.
func (b *botEntry) setSharedPublicData(widgetID, data string) {
	for _, l := range b.chatLogs() {
		l.mu.Lock()
		for i := range l.msgs {
			if w := l.msgs[i].Widget; w != nil && w.ID == widgetID {
				updated := *w
				updated.PublicData = data
				l.msgs[i].Widget = &updated
			}
		}
		l.mu.Unlock()
	}
}

// SendWidget stores a message with an HTML widget (Bot API 10.4).
func (h *Handlers) SendWidget(c *gin.Context, bot *botEntry) {
	body, chatID, ok := readWidgetSend(c)
	if !ok {
		return
	}
	w := Widget{
		HTML:         toString(body["html"]),
		Width:        toInt(body["width"]),
		Height:       toInt(body["height"]),
		PublicData:   toString(body["public_data"]),
		ChatData:     toString(body["chat_data"]),
		AllowQueries: toBool(body["allow_queries"]),
	}
	if err := checkWidgetHTML(w.HTML); err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	if err := checkWidgetData("public_data", w.PublicData); err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	if err := checkWidgetData("chat_data", w.ChatData); err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	replyTo, err := replyToMessageID(body)
	if err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	if toBool(body["is_shared"]) {
		w.ID = "widget_" + h.Store.NextQueryID()
		shared := w
		shared.ChatData = ""
		bot.widgets.addShared(w.ID, shared)
	}
	h.storeWidgetMessage(c, bot, body, chatID, replyTo, w)
}

// SendSharedWidget sends again a shared widget created by this bot.
func (h *Handlers) SendSharedWidget(c *gin.Context, bot *botEntry) {
	body, chatID, ok := readWidgetSend(c)
	if !ok {
		return
	}
	widgetID := toString(body["widget_id"])
	if widgetID == "" {
		errResp(c, http.StatusBadRequest, "Bad Request: widget_id is required")
		return
	}
	chatData := toString(body["chat_data"])
	if err := checkWidgetData("chat_data", chatData); err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	replyTo, err := replyToMessageID(body)
	if err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	// Shared widgets are per bot, so another bot's widget_id is not found.
	w, found := bot.widgets.lookupShared(widgetID)
	if !found {
		errResp(c, http.StatusBadRequest, "Bad Request: widget not found")
		return
	}
	w.ChatData = chatData
	h.storeWidgetMessage(c, bot, body, chatID, replyTo, w)
}

// readWidgetSend parses a widget send and checks its target chat and
// caption. In production widgets can be sent only to private chats, whose
// ids are positive; group ids are negative and @usernames name public chats.
func readWidgetSend(c *gin.Context) (map[string]any, int64, bool) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return nil, 0, false
	}
	raw, present := body["chat_id"]
	if !present || raw == "" {
		errResp(c, http.StatusBadRequest, "Bad Request: chat_id is required")
		return nil, 0, false
	}
	chatID := toInt64(raw)
	if chatID <= 0 {
		errResp(c, http.StatusBadRequest, "Bad Request: widgets can be sent only to private chats")
		return nil, 0, false
	}
	// Telegram counts the caption after entities parsing; the mock does not
	// parse it, so parse_mode markup counts towards the limit.
	if utf8.RuneCountInString(toString(body["caption"])) > maxWidgetCaption {
		errResp(c, http.StatusBadRequest, "Bad Request: message caption is too long")
		return nil, 0, false
	}
	return body, chatID, true
}

func (h *Handlers) storeWidgetMessage(c *gin.Context, bot *botEntry, body map[string]any, chatID int64, replyTo int, w Widget) {
	m := h.newBotMessage(bot, chatID)
	m.ReplyToMessage = bot.replyTarget(m, replyTo)
	setMessageThread(&m, body)
	m.Caption = toString(body["caption"])
	m.CaptionEntities = parseEntities(body["caption_entities"])
	m.ParseMode = toString(body["parse_mode"])
	m.ShowCaptionAboveMedia = toBool(body["show_caption_above_media"])
	m.ReplyMarkup = parseReplyMarkup(body["reply_markup"])
	// Sent widgets always have protected content.
	m.HasProtectedContent = true
	m.Widget = &w
	bot.AppendMessage(m)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: m})
}

// EditMessageWidgetData changes chat_data and public_data of a widget in a
// bot message. Public data of a shared widget changes on every message
// with it.
func (h *Handlers) EditMessageWidgetData(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	// Inline messages are not stored, as in the other edit handlers.
	if toString(body["inline_message_id"]) != "" {
		c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
		return
	}
	chatID, messageID := toInt64(body["chat_id"]), toInt(body["message_id"])
	if chatID == 0 || messageID == 0 {
		errResp(c, http.StatusBadRequest, "Bad Request: chat_id and message_id are required")
		return
	}
	publicData, setPublic := optionalString(body, "public_data")
	chatData, setChat := optionalString(body, "chat_data")
	if err := checkWidgetData("public_data", publicData); err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	if err := checkWidgetData("chat_data", chatData); err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	m, found := bot.latestWidgetMessage(chatID, messageID)
	if !found {
		errResp(c, http.StatusBadRequest, "Bad Request: message to edit not found")
		return
	}
	w := *m.Widget
	if (!setPublic || publicData == w.PublicData) && (!setChat || chatData == w.ChatData) {
		errResp(c, http.StatusBadRequest, "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message")
		return
	}
	if setPublic && publicData != w.PublicData && w.ID != "" {
		if !h.editSharedPublicData(c, bot, w.ID, publicData) {
			return
		}
	}
	if setChat {
		w.ChatData = chatData
	}
	if setPublic {
		w.PublicData = publicData
	}
	// Store the edit as a full revision, like the other edit handlers.
	m.Widget = &w
	m.Date = time.Now().Unix()
	m.isNew = false
	bot.AppendMessage(m)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
}

// EditSharedWidgetData changes public data of a shared widget everywhere.
func (h *Handlers) EditSharedWidgetData(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	widgetID := toString(body["widget_id"])
	if widgetID == "" {
		errResp(c, http.StatusBadRequest, "Bad Request: widget_id is required")
		return
	}
	publicData, ok := optionalString(body, "public_data")
	if !ok {
		errResp(c, http.StatusBadRequest, "Bad Request: public_data is required")
		return
	}
	if err := checkWidgetData("public_data", publicData); err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	if raw, ok := body["cache_time"]; ok {
		if ct := toInt(raw); ct < 5 || ct > 3600 {
			errResp(c, http.StatusBadRequest, "Bad Request: cache_time must be between 5 and 3600")
			return
		}
	}
	if h.editSharedPublicData(c, bot, widgetID, publicData) {
		c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
	}
}

// editSharedPublicData applies a shared widget public data edit, answering
// the request itself when the widget is unknown or edited too often.
func (h *Handlers) editSharedPublicData(c *gin.Context, bot *botEntry, widgetID, data string) bool {
	wait, found := bot.widgets.setPublicData(widgetID, data, time.Now())
	if !found {
		errResp(c, http.StatusBadRequest, "Bad Request: widget not found")
		return false
	}
	if wait > 0 {
		retryAfter := max(1, int(wait.Round(time.Second)/time.Second))
		c.JSON(http.StatusTooManyRequests, APIResponse{
			OK:          false,
			ErrorCode:   http.StatusTooManyRequests,
			Description: "Too Many Requests: retry after " + strconv.Itoa(retryAfter),
			Parameters:  &ResponseParameters{RetryAfter: retryAfter},
		})
		return false
	}
	bot.setSharedPublicData(widgetID, data)
	return true
}

// AnswerWidgetQuery accepts one answer per injected query within 15 s. The
// request journal records the answer for scenarios to await.
func (h *Handlers) AnswerWidgetQuery(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	queryID := toString(body["widget_query_id"])
	if queryID == "" {
		errResp(c, http.StatusBadRequest, "Bad Request: widget_query_id is required")
		return
	}
	if err := checkWidgetData("data", toString(body["data"])); err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	if !bot.widgets.answerQuery(queryID, time.Now()) {
		errResp(c, http.StatusBadRequest, errQueryInvalid)
		return
	}
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
}

// fillWidgetQuery builds an injected widget_query or shared_widget_query.
// The widget must exist and have been sent with allow_queries; queries over
// the per-user limit return errWidgetQueryFlood and are not delivered.
func (h *Handlers) fillWidgetQuery(upd *Update, bot *botEntry, req *InjectUpdateRequest, from User, chat Chat) error {
	if n := len(req.WidgetData); n == 0 || n > maxWidgetQueryData {
		return fmt.Errorf("widget_data must be 1-%d bytes", maxWidgetQueryData)
	}
	queryType := WidgetQueryType{Type: "data"}
	id := h.Store.NextQueryID()
	if req.Type == "shared_widget_query" {
		if req.WidgetID == "" {
			return errors.New("widget_id is required")
		}
		w, ok := bot.widgets.lookupShared(req.WidgetID)
		if !ok {
			return errors.New("shared widget not found")
		}
		if !w.AllowQueries {
			return errors.New("widget was sent without allow_queries")
		}
		upd.SharedWidgetQuery = &SharedWidgetQuery{
			ID: id, From: from, Type: queryType, Data: req.WidgetData, WidgetID: req.WidgetID,
		}
	} else {
		if req.MessageID == 0 {
			return errors.New("message_id is required")
		}
		m, ok := bot.latestWidgetMessage(chat.ID, req.MessageID)
		if !ok {
			return errors.New("no widget message with this message_id")
		}
		if !m.Widget.AllowQueries {
			return errors.New("widget was sent without allow_queries")
		}
		upd.WidgetQuery = &WidgetQuery{
			ID:           id,
			From:         from,
			Chat:         &chat,
			MessageID:    req.MessageID,
			ChatInstance: strconv.FormatInt(chat.ID, 10),
			Type:         queryType,
			Data:         req.WidgetData,
			WidgetID:     m.Widget.ID,
		}
	}
	now := time.Now()
	if !bot.widgets.allowQuery(from.ID, now) {
		upd.WidgetQuery, upd.SharedWidgetQuery = nil, nil
		return errWidgetQueryFlood
	}
	bot.widgets.registerQuery(id, now)
	return nil
}

func checkWidgetHTML(s string) error {
	if s == "" || len(s) > maxWidgetHTML {
		return fmt.Errorf("widget html must be 1-%d bytes", maxWidgetHTML)
	}
	// Telegram compresses with PHP's gzcompress; Go's zlib output differs
	// by a few bytes, so payloads right at the limit may disagree.
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	_, _ = zw.Write([]byte(s))
	_ = zw.Close()
	if buf.Len() > maxWidgetHTMLCompressed {
		return fmt.Errorf("widget html must not exceed %d bytes compressed", maxWidgetHTMLCompressed)
	}
	return nil
}

func checkWidgetData(name, s string) error {
	if len(s) > maxWidgetData {
		return fmt.Errorf("%s must be at most %d bytes", name, maxWidgetData)
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	return nil
}

// optionalString reads a string parameter that may be omitted; an empty
// string is a value (it removes the data), not an omission.
func optionalString(body map[string]any, key string) (string, bool) {
	v, ok := body[key]
	if !ok || v == nil {
		return "", false
	}
	return toString(v), true
}

// renderWidget summarizes a widget for the debug chat: the mock does not
// run widget HTML.
func renderWidget(w *Widget) template.HTML {
	var b bytes.Buffer
	b.WriteString(`<div class="widget-data">`)
	fmt.Fprintf(&b, "html %d B", len(w.HTML))
	if w.Width > 0 || w.Height > 0 {
		fmt.Fprintf(&b, " · %d×%d", w.Width, w.Height)
	}
	if w.AllowQueries {
		b.WriteString(" · queries")
	}
	for _, field := range []struct{ name, value string }{
		{"public_data", w.PublicData}, {"chat_data", w.ChatData},
	} {
		if field.value != "" {
			b.WriteString("<br>" + field.name + ": <code>" + html.EscapeString(field.value) + "</code>")
		}
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}
