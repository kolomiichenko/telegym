package mock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// Handlers wires the Bot API method endpoints to the Store and UpdateDispatcher.
// Every method either returns a valid Message (so telego et al. can
// unmarshal) or a boolean - matching the real Telegram API contract.
type Handlers struct {
	Store            *Store
	UpdateDispatcher *UpdateDispatcher
}

// errResp writes a Telegram-shaped error envelope.
func errResp(c *gin.Context, code int, desc string) {
	c.JSON(code, APIResponse{OK: false, ErrorCode: code, Description: desc})
}

// readBody returns either parsed JSON body or, for multipart and urlencoded
// requests, a map of form fields. The handler decides which keys it cares about.
func readBody(c *gin.Context) (map[string]any, error) {
	ct := c.GetHeader("Content-Type")
	multipart := strings.HasPrefix(ct, "multipart/form-data")
	if multipart || strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		var err error
		if multipart {
			err = c.Request.ParseMultipartForm(32 << 20)
		} else {
			err = c.Request.ParseForm()
		}
		if err != nil {
			return nil, err
		}
		out := make(map[string]any, len(c.Request.PostForm))
		for k, v := range c.Request.PostForm {
			if len(v) == 1 {
				out[k] = v[0]
			} else {
				out[k] = v
			}
		}
		return out, nil
	}
	if c.Request.ContentLength == 0 {
		return map[string]any{}, nil
	}
	body, err := c.GetRawData()
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func toInt(v any) int {
	return int(toInt64(v))
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// rawJSON reads a structured parameter from JSON or a JSON-encoded form field.
func rawJSON(v any) ([]byte, error) {
	if s, ok := v.(string); ok {
		return []byte(s), nil
	}
	return json.Marshal(v)
}

func parseReplyMarkup(v any) *ReplyMarkup {
	if v == nil {
		return nil
	}
	// reply_markup over multipart arrives as JSON string; over JSON as object.
	var raw []byte
	switch x := v.(type) {
	case string:
		raw = []byte(x)
	default:
		var err error
		raw, err = json.Marshal(v)
		if err != nil {
			return nil
		}
	}
	var m ReplyMarkup
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return &m
}

func parseEntities(v any) []Entity {
	if v == nil {
		return nil
	}
	var raw []byte
	switch x := v.(type) {
	case string:
		raw = []byte(x)
	default:
		var err error
		raw, err = json.Marshal(v)
		if err != nil {
			return nil
		}
	}
	var ents []Entity
	if err := json.Unmarshal(raw, &ents); err != nil {
		return nil
	}
	return ents
}

// newBotMessage builds a baseline Message authored by the given bot at "now".
// It is marked new so storing it ends the sendChatAction status in its
// topic: in Telegram a new message does that, an edit does not.
func (h *Handlers) newBotMessage(bot *botEntry, chatID int64) Message {
	return Message{
		isNew:     true,
		MessageID: bot.NextMessageID(chatID),
		From:      &bot.identity,
		Date:      time.Now().Unix(),
		Chat:      Chat{ID: chatID, Type: "private"},
	}
}

// fakeFileID generates a stable-looking file_id for media we never store.
func fakeFileID(kind string) string {
	return fmt.Sprintf("MOCK_%s_%d", strings.ToUpper(kind), time.Now().UnixNano())
}

// =====================================================================
// Bot API endpoints
// =====================================================================

func (h *Handlers) GetUpdates(c *gin.Context, bot *botEntry) {
	if h.UpdateDispatcher.hasTarget(bot) {
		errResp(c, http.StatusConflict, errPollWebhook.Error())
		return
	}
	// Same tolerant parsing as other methods: empty body, query/form fields,
	// and string-encoded numbers are all accepted by Telegram.
	body, err := readBody(c)
	if err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	param := func(key string) any {
		if v, ok := body[key]; ok {
			return v
		}
		return c.Query(key)
	}

	// The newest request wins: a previous long poll is terminated with 409.
	ctx, release := bot.updates.BeginPoll(c.Request.Context())
	defer release()

	timeout := time.Duration(max(0, toInt(param("timeout")))) * time.Second
	updates := bot.updates.Poll(ctx, toInt64(param("offset")), toInt(param("limit")), timeout)
	var conflict pollConflict
	if errors.As(context.Cause(ctx), &conflict) {
		errResp(c, http.StatusConflict, conflict.Error())
		return
	}
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: updates})
}

func (h *Handlers) GetMe(c *gin.Context, bot *botEntry) {
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: bot.identity})
}

func (h *Handlers) SendMessage(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, 400, "Bad Request: "+err.Error())
		return
	}
	chatID := toInt64(body["chat_id"])
	if chatID == 0 {
		errResp(c, 400, "Bad Request: chat_id is required")
		return
	}

	m := h.newBotMessage(bot, chatID)
	m.Text = toString(body["text"])
	m.Entities = parseEntities(body["entities"])
	setMessageThread(&m, body)
	m.ParseMode = toString(body["parse_mode"])
	m.ReplyMarkup = parseReplyMarkup(body["reply_markup"])

	bot.AppendMessage(m)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: m})
}

func (h *Handlers) EditMessageText(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, 400, "Bad Request: "+err.Error())
		return
	}
	chatID := toInt64(body["chat_id"])
	m := Message{
		MessageID:   toInt(body["message_id"]),
		From:        &bot.identity,
		Date:        time.Now().Unix(),
		Chat:        Chat{ID: chatID, Type: "private"},
		Text:        toString(body["text"]),
		Entities:    parseEntities(body["entities"]),
		ReplyMarkup: parseReplyMarkup(body["reply_markup"]),
		ParseMode:   toString(body["parse_mode"]),
	}
	bot.AppendMessage(m)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: m})
}

func (h *Handlers) EditMessageReplyMarkup(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, 400, "Bad Request: "+err.Error())
		return
	}
	m := Message{
		MessageID:   toInt(body["message_id"]),
		From:        &bot.identity,
		Date:        time.Now().Unix(),
		Chat:        Chat{ID: toInt64(body["chat_id"]), Type: "private"},
		ReplyMarkup: parseReplyMarkup(body["reply_markup"]),
	}
	bot.AppendMessage(m)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: m})
}

func (h *Handlers) EditMessageCaption(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	if toString(body["inline_message_id"]) != "" {
		c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
		return
	}

	// Like other edit handlers, record an event for the original message ID
	// rather than replacing the previous entry in the outbound history.
	m := Message{
		MessageID:       toInt(body["message_id"]),
		From:            &bot.identity,
		Date:            time.Now().Unix(),
		Chat:            Chat{ID: toInt64(body["chat_id"]), Type: "private"},
		Caption:         toString(body["caption"]),
		CaptionEntities: parseEntities(body["caption_entities"]),
		ReplyMarkup:     parseReplyMarkup(body["reply_markup"]),
		ParseMode:       toString(body["parse_mode"]),
	}
	bot.AppendMessage(m)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: m})
}

func (h *Handlers) EditMessageMedia(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, 400, "Bad Request: "+err.Error())
		return
	}
	m := Message{
		MessageID:   toInt(body["message_id"]),
		From:        &bot.identity,
		Date:        time.Now().Unix(),
		Chat:        Chat{ID: toInt64(body["chat_id"]), Type: "private"},
		Caption:     toString(body["caption"]),
		ReplyMarkup: parseReplyMarkup(body["reply_markup"]),
		Photo:       []PhotoSize{{FileID: fakeFileID("photo"), FileUniqueID: fakeFileID("photo_u"), Width: 1, Height: 1}},
	}
	bot.AppendMessage(m)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: m})
}

// DeleteMessage drops the message from the store so the debug chat stops
// rendering it. The store only holds the bot's outbound messages, so a miss
// (deleting a user's message, or one already evicted) still returns true.
func (h *Handlers) DeleteMessage(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	msgID := toInt(body["message_id"])
	if msgID == 0 {
		errResp(c, http.StatusBadRequest, "Bad Request: message_id is required")
		return
	}
	// chat_id may be a @channel username, which the store does not index.
	if chatID := toInt64(body["chat_id"]); chatID != 0 {
		bot.DeleteMessage(chatID, msgID)
	}
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
}

func (h *Handlers) AnswerCallbackQuery(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err == nil {
		text := toString(body["text"])
		if text != "" {
			cbID := toString(body["callback_query_id"])
			showAlert, _ := body["show_alert"].(bool)
			if chatID := bot.LookupCallbackChat(cbID); chatID != 0 {
				bot.PushToast(chatID, Toast{
					Text:      text,
					ShowAlert: showAlert,
					At:        time.Now().Unix(),
				})
			}
		}
	}
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
}

func (h *Handlers) sendMedia(c *gin.Context, bot *botEntry, kind string) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, 400, "Bad Request: "+err.Error())
		return
	}
	chatID := toInt64(body["chat_id"])
	if chatID == 0 {
		errResp(c, 400, "Bad Request: chat_id is required")
		return
	}
	m := h.newBotMessage(bot, chatID)
	m.Caption = toString(body["caption"])
	m.Text = m.Caption
	setMessageThread(&m, body)
	m.Entities = parseEntities(body["caption_entities"])
	m.ParseMode = toString(body["parse_mode"])
	m.ReplyMarkup = parseReplyMarkup(body["reply_markup"])

	// If this was a multipart upload, capture the actual bytes into the
	// FileStore so telegym-proxy can later forward the real file to Telegram.
	// For file_id-only requests (cached), we keep the existing fake ID.
	fileID, err := h.captureUploadedFile(c, kind)
	if err != nil {
		if errors.Is(err, errUploadTooLarge) || errors.Is(err, errFileStoreTooSmall) {
			errResp(c, http.StatusRequestEntityTooLarge, "Request Entity Too Large: "+err.Error())
			return
		}
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	if fileID == "" {
		// Fall back to whatever was passed in the body as file reference,
		// or a fresh fake ID if nothing was provided.
		if ref := toString(body[kind]); ref != "" {
			fileID = ref
		} else {
			fileID = fakeFileID(kind)
		}
	}
	uniqueID := fileID + "_u"

	switch kind {
	case "photo":
		m.Photo = []PhotoSize{{FileID: fileID, FileUniqueID: uniqueID, Width: 1, Height: 1}}
	case "video":
		m.Video = &Video{FileID: fileID, FileUniqueID: uniqueID, Width: 1, Height: 1, Duration: 1}
	case "animation":
		m.Animation = &Animation{FileID: fileID, FileUniqueID: uniqueID, Width: 1, Height: 1, Duration: 1}
	case "sticker":
		// Real Telegram stores an emoji per sticker in its sticker-set DB
		// and returns it on every send. The mock has no such DB, so we
		// only know an emoji when the bot explicitly passes one in the
		// request. Leave the field empty otherwise - the debug UI will
		// surface the absence so it's obvious nothing was supplied,
		// instead of hiding it behind a default.
		m.Sticker = &Sticker{
			FileID: fileID, FileUniqueID: uniqueID,
			Type: "regular", Width: 1, Height: 1,
			Emoji: toString(body["emoji"]),
		}
	}
	bot.AppendMessage(m)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: m})
}

var (
	errUploadTooLarge    = errors.New("uploaded file exceeds 50 MB")
	errFileStoreTooSmall = errors.New("uploaded file exceeds file store capacity")
)

// captureUploadedFile stores the named multipart file and returns its file_id.
// It returns an empty ID when no file was uploaded, or an error if capture fails.
func (h *Handlers) captureUploadedFile(c *gin.Context, fieldName string) (string, error) {
	ct := c.GetHeader("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data") {
		return "", nil
	}
	fileHdr, err := c.FormFile(fieldName)
	if errors.Is(err, http.ErrMissingFile) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	src, err := fileHdr.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = src.Close() }()
	const maxFile = 50 * 1024 * 1024 // 50 MB per file
	limit := min(int64(maxFile), h.Store.Files.maxBytes)
	data, err := io.ReadAll(io.LimitReader(src, limit+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxFile {
		return "", errUploadTooLarge
	}
	// Reject before Put: an upload larger than capacity would evict the
	// existing files and itself, leaving the client with an unusable ID.
	if int64(len(data)) > h.Store.Files.maxBytes {
		return "", errFileStoreTooSmall
	}
	ctype := fileHdr.Header.Get("Content-Type")
	return h.Store.Files.Put(fieldName, ctype, fileHdr.Filename, data), nil
}

func (h *Handlers) SendPhoto(c *gin.Context, b *botEntry)     { h.sendMedia(c, b, "photo") }
func (h *Handlers) SendVideo(c *gin.Context, b *botEntry)     { h.sendMedia(c, b, "video") }
func (h *Handlers) SendAnimation(c *gin.Context, b *botEntry) { h.sendMedia(c, b, "animation") }
func (h *Handlers) SendSticker(c *gin.Context, b *botEntry)   { h.sendMedia(c, b, "sticker") }

func (h *Handlers) SendDice(c *gin.Context, bot *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, 400, "Bad Request: "+err.Error())
		return
	}
	chatID := toInt64(body["chat_id"])
	emoji := toString(body["emoji"])
	if emoji == "" {
		emoji = "🎲"
	}
	// Real Telegram returns a random 1..6 (1..64 for slot etc). For load
	// tests, a deterministic-ish value derived from time is fine and avoids
	// pulling in math/rand state contention.
	value := int(time.Now().UnixNano()%6) + 1

	m := h.newBotMessage(bot, chatID)
	m.Dice = &Dice{Emoji: emoji, Value: value}
	setMessageThread(&m, body)
	bot.AppendMessage(m)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: m})
}

func (h *Handlers) GetChatMember(c *gin.Context, _ *botEntry) {
	body, err := readBody(c)
	if err != nil {
		errResp(c, 400, "Bad Request: "+err.Error())
		return
	}
	userID := toInt64(body["user_id"])
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: ChatMember{
		Status: "member",
		User: User{
			ID:        userID,
			IsBot:     false,
			FirstName: "Mock User",
		},
	}})
}

// Telegram accepts an empty JSON body as no parameters for both webhook methods.
// deleteWebhook has no required parameters. Although setWebhook documents url
// as required, its server treats an omitted URL as empty and removes the webhook.
// Accept io.EOF from binding below to match that behavior.
// See process_set_webhook_query in:
// https://github.com/tdlib/telegram-bot-api/blob/e3e9dd8e5b3d7ab8537cd5a10dc31d5ffa8f82d1/telegram-bot-api/Client.cpp#L15803-L15807
type webhookRequest struct {
	URL                string `json:"url" form:"url"`
	SecretToken        string `json:"secret_token" form:"secret_token"`
	DropPendingUpdates bool   `json:"drop_pending_updates" form:"drop_pending_updates"`
}

func (h *Handlers) SetWebhook(c *gin.Context, bot *botEntry) {
	var req webhookRequest
	if err := c.ShouldBind(&req); err != nil && !errors.Is(err, io.EOF) {
		errResp(c, 400, "Bad Request: "+err.Error())
		return
	}
	if req.DropPendingUpdates {
		bot.updates.Clear()
	}
	bot.SetWebhook(req.URL, req.SecretToken)
	if req.URL != "" {
		// Telegram ends an active long poll once a webhook is registered.
		bot.updates.TerminatePoll(errPollWebhookSet)
	} else {
		bot.updates.OptIn()
	}
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
}

func (h *Handlers) DeleteWebhook(c *gin.Context, bot *botEntry) {
	var req webhookRequest
	if err := c.ShouldBind(&req); err != nil && !errors.Is(err, io.EOF) {
		errResp(c, http.StatusBadRequest, "Bad Request: "+err.Error())
		return
	}
	if req.DropPendingUpdates {
		bot.updates.Clear()
	}
	bot.SetWebhook("", "")
	// Bots call deleteWebhook before switching to getUpdates.
	bot.updates.OptIn()
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
}

func (h *Handlers) GetWebhookInfo(c *gin.Context, bot *botEntry) {
	url, _ := h.UpdateDispatcher.target(bot)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: WebhookInfo{
		URL:                  url,
		HasCustomCertificate: false,
		PendingUpdateCount:   bot.updates.Len(),
		MaxConnections:       40,
	}})
}

// GenericOK handles methods without a custom implementation but that bots
// commonly call. Always returns {ok:true, result:true} which is the right
// shape for the most common boolean-returning methods (pinChatMessage,
// promoteChatMember, etc).
func (h *Handlers) GenericOK(c *gin.Context, _ *botEntry) {
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
}

// chatActionLabels maps every sendChatAction action to the status line a
// Telegram client shows while it is active.
var chatActionLabels = map[string]string{
	"typing":            "typing…",
	"upload_photo":      "sending a photo…",
	"record_video":      "recording a video…",
	"upload_video":      "sending a video…",
	"record_voice":      "recording a voice message…",
	"upload_voice":      "sending a voice message…",
	"upload_document":   "sending a file…",
	"choose_sticker":    "choosing a sticker…",
	"find_location":     "finding a location…",
	"record_video_note": "recording a video message…",
	"upload_video_note": "sending a video message…",
}

// SendChatAction records the action so /debug/chat can show the bot's
// "typing…" status until its next message or the 5s timeout.
func (h *Handlers) SendChatAction(c *gin.Context, bot *botEntry) {
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
	action := toString(body["action"])
	if _, ok := chatActionLabels[action]; !ok {
		errResp(c, http.StatusBadRequest, "Bad Request: wrong parameter action in request")
		return
	}
	bot.SetTyping(chatID, toInt(body["message_thread_id"]), action)
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: true})
}

// setMessageThread copies message_thread_id from a send* request, marking
// the message as sent to a forum topic.
func setMessageThread(m *Message, body map[string]any) {
	if tid := toInt(body["message_thread_id"]); tid != 0 {
		m.MessageThreadID = tid
		m.IsTopicMessage = true
	}
}

// forumTopicIconColors are the icon_color values createForumTopic accepts.
var forumTopicIconColors = map[int]bool{
	0x6FB9F0: true, 0xFFD67E: true, 0xCB86DB: true,
	0x8EEE98: true, 0xFF93B2: true, 0xFB6F5F: true,
}

// CreateForumTopic opens a topic and posts its forum_topic_created service
// message, so the debug chat can show and filter by topic.
func (h *Handlers) CreateForumTopic(c *gin.Context, bot *botEntry) {
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
	name := toString(body["name"])
	if n := utf8.RuneCountInString(name); n == 0 || n > 128 {
		errResp(c, http.StatusBadRequest, "Bad Request: invalid forum topic name specified")
		return
	}
	iconColor := 0x6FB9F0
	if _, ok := body["icon_color"]; ok {
		iconColor = toInt(body["icon_color"])
		if !forumTopicIconColors[iconColor] {
			errResp(c, http.StatusBadRequest, "Bad Request: invalid icon_color specified")
			return
		}
	}
	topic := bot.CreateForumTopic(chatID, name, iconColor, toString(body["icon_custom_emoji_id"]))
	c.JSON(http.StatusOK, APIResponse{OK: true, Result: topic})
}
