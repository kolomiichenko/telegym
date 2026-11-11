package mock

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// DebugAPI exposes endpoints used by the xk6 extension (and humans via curl)
// to drive the bot: inject Updates and inspect what the bot has sent.

// InjectUpdateRequest describes a synthetic Update delivered by webhook or polling.
type InjectUpdateRequest struct {
	Token string `json:"token" binding:"required"`

	// Type defaults to message, or callback_query when callback_data is nonempty.
	Type string `json:"type"`

	// Message-shaped update
	UserID    int64  `json:"user_id"`
	ChatID    int64  `json:"chat_id"`
	ChatType  string `json:"chat_type"`
	ChatTitle string `json:"chat_title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	Text      string `json:"text"`
	MessageID int    `json:"message_id"` // original message for edits and callbacks

	// Callback-shaped update
	CallbackData string `json:"callback_data"`

	// Chat-member-shaped update, for chat_member and my_chat_member. ActorID
	// defaults to the affected user and applies to chat_member only. Only
	// member, left and kicked statuses are supported.
	ActorID   int64  `json:"actor_id"`
	OldStatus string `json:"old_status"`
	NewStatus string `json:"new_status"`

	// Media for message and edited_message; at most one of them. Files are
	// references only: the mock holds no bytes for these file_ids.
	Caption  string      `json:"caption"`
	Photo    []PhotoSize `json:"photo"`
	Document *Document   `json:"document"`
	Contact  *Contact    `json:"contact"`
	Location *Location   `json:"location"`
	Sticker  *Sticker    `json:"sticker"`

	// Inline-shaped updates. ResultID is for chosen_inline_result.
	Query    string `json:"query"`
	Offset   string `json:"offset"`
	ResultID string `json:"result_id"`

	// Payment-shaped updates. Currency defaults to XTR (Telegram Stars).
	Currency       string `json:"currency"`
	TotalAmount    int    `json:"total_amount"`
	InvoicePayload string `json:"invoice_payload"`

	// Reaction-shaped update: emoji reactions on MessageID.
	OldReaction []string `json:"old_reaction"`
	NewReaction []string `json:"new_reaction"`

	// Poll-answer-shaped update. Empty OptionIDs retracts the vote.
	PollID    string `json:"poll_id"`
	OptionIDs []int  `json:"option_ids"`

	// Widget-query-shaped updates: widget_query targets the widget in
	// MessageID, shared_widget_query the shared widget WidgetID.
	WidgetData string `json:"widget_data"`
	WidgetID   string `json:"widget_id"`
}

func (h *Handlers) DebugInjectUpdate(c *gin.Context) {
	var req InjectUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	bot := h.Store.Bot(req.Token)
	if req.UserID == 0 {
		req.UserID = req.ChatID
	}
	if req.ChatID == 0 {
		req.ChatID = req.UserID
	}
	if req.FirstName == "" {
		req.FirstName = "load-test user"
	}
	if req.Username == "" {
		req.Username = "user_" + strconv.FormatInt(req.UserID, 10)
	}

	if req.ChatType == "" {
		req.ChatType = "private"
	}
	if req.Type == "" {
		if req.CallbackData != "" {
			req.Type = "callback_query"
		} else {
			req.Type = "message"
		}
	}

	from := User{ID: req.UserID, IsBot: false, FirstName: req.FirstName, Username: req.Username}
	chat := Chat{ID: req.ChatID, Type: req.ChatType, Title: req.ChatTitle}
	userChat := chat // the chat as seen in updates the user originates
	if chat.Type == "private" {
		userChat.FirstName = req.FirstName
		userChat.Username = req.Username
	}
	var upd Update // UpdateID is assigned on delivery

	switch req.Type {
	case "callback_query":
		cbID := strconv.FormatInt(time.Now().UnixNano(), 10)
		upd.CallbackQuery = &CallbackQuery{
			ID:           cbID,
			From:         from,
			ChatInstance: strconv.FormatInt(req.ChatID, 10),
			Data:         req.CallbackData,
			Message: &Message{
				MessageID: req.MessageID,
				From:      &bot.identity,
				Chat:      chat,
				Date:      time.Now().Unix(),
			},
		}
		// Register cb_id → chat_id so the bot's eventual
		// answerCallbackQuery(cb_id, text=...) can route the toast back
		// to the correct chat for the debug UI.
		bot.RegisterCallback(cbID, req.ChatID)
	case "message", "edited_message":
		messageID := req.MessageID
		if req.Type == "message" || messageID == 0 {
			messageID = bot.NextMessageID(chat.ID)
		}
		message := &Message{
			MessageID: messageID,
			From:      &from,
			Date:      time.Now().Unix(),
			Chat:      userChat,
			Text:      req.Text,
		}
		if err := attachMedia(message, &req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if req.Type == "edited_message" {
			upd.EditedMessage = message
		} else {
			upd.Message = message
		}
	case "widget_query", "shared_widget_query":
		if _, err := fillWidgetQuery(h.Store, &upd, bot, &req, from, userChat); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errWidgetQueryFlood) {
				status = http.StatusTooManyRequests
			}
			c.JSON(status, gin.H{"error": err.Error()})
			return
		}
	case "chat_member":
		if !supportedMemberStatus(req.OldStatus) || !supportedMemberStatus(req.NewStatus) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "old_status and new_status must be member, left or kicked"})
			return
		}
		actor := from
		if req.ActorID != 0 && req.ActorID != req.UserID {
			actor = User{ID: req.ActorID, FirstName: "update actor"}
		}
		upd.ChatMember = &ChatMemberUpdated{
			Chat:          chat,
			From:          actor,
			Date:          time.Now().Unix(),
			OldChatMember: chatMemberWithStatus(from, req.OldStatus),
			NewChatMember: chatMemberWithStatus(from, req.NewStatus),
		}
	default:
		if err := h.fillUpdate(&upd, bot, &req, from, userChat); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	res, err := h.UpdateDispatcher.DeliverUpdate(c.Request.Context(), bot, upd)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"ok":              false,
			"update_id":       res.UpdateID,
			"delivery_status": res.Status,
			"error":           err.Error(),
		})
		return
	}
	resp := gin.H{
		"ok":              true,
		"update_id":       res.UpdateID,
		"delivery_status": res.Status,
		"queued":          res.Queued,
	}
	if upd.CallbackQuery != nil {
		resp["callback_query_id"] = upd.CallbackQuery.ID
	}
	if upd.InlineQuery != nil {
		resp["inline_query_id"] = upd.InlineQuery.ID
	}
	if upd.PreCheckoutQuery != nil {
		resp["pre_checkout_query_id"] = upd.PreCheckoutQuery.ID
	}
	if upd.WidgetQuery != nil {
		resp["widget_query_id"] = upd.WidgetQuery.ID
	}
	if upd.SharedWidgetQuery != nil {
		resp["widget_query_id"] = upd.SharedWidgetQuery.ID
	}
	c.JSON(http.StatusOK, resp)
}

func supportedMemberStatus(status string) bool {
	// Other member variants require fields the injection API cannot supply.
	return status == "member" || status == "left" || status == "kicked"
}

func chatMemberWithStatus(user User, status string) ChatMember {
	member := ChatMember{Status: status, User: user}
	if status == "kicked" {
		member.UntilDate = new(int64)
	}
	return member
}

// DebugListMessages returns outbound messages stored for a bot, optionally
// filtered by chat_id and since (unix seconds). Used by xk6 to await replies.
func (h *Handlers) DebugListMessages(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.JSON(400, gin.H{"error": "token path param required"})
		return
	}
	bot := h.Store.Bot(token)

	chatID, _ := strconv.ParseInt(c.Query("chat_id"), 10, 64)
	since, _ := strconv.ParseInt(c.Query("since"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))

	var messages []Message
	if since > 0 {
		messages = bot.MessagesSince(chatID, since)
	} else {
		messages = bot.Messages(chatID, limit)
	}
	c.JSON(http.StatusOK, gin.H{
		"chat_id":  chatID,
		"count":    len(messages),
		"messages": messages,
	})
}

func (h *Handlers) DebugClearMessages(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		c.JSON(400, gin.H{"error": "token path param required"})
		return
	}
	bot := h.Store.Bot(token)
	n := bot.ClearMessages()
	c.JSON(http.StatusOK, gin.H{"cleared": n})
}

// DebugListBots returns a snapshot of every bot the mock has seen since
// startup, most-recently-active first. Powers the bot inventory on the
// /debug/chat home page.
func (h *Handlers) DebugListBots(c *gin.Context) {
	bots := h.Store.Bots()
	c.JSON(http.StatusOK, gin.H{
		"count": len(bots),
		"bots":  bots,
	})
}
