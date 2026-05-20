package mock

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// DebugAPI exposes endpoints used by the xk6 extension (and humans via curl)
// to drive the bot: inject Updates and inspect what the bot has sent.

// InjectUpdateRequest describes a synthetic Update the load runner wants to
// deliver to the bot via webhook. Either Message or CallbackQuery is required.
type InjectUpdateRequest struct {
	Token string `json:"token" binding:"required"`

	// Message-shaped update
	UserID    int64  `json:"user_id"`
	ChatID    int64  `json:"chat_id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	Text      string `json:"text"`

	// Callback-shaped update
	CallbackData string `json:"callback_data"`
	MessageID    int    `json:"message_id"` // message the callback button belongs to
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

	from := User{ID: req.UserID, IsBot: false, FirstName: req.FirstName, Username: req.Username}
	upd := Update{UpdateID: h.Store.NextUpdateID()}

	switch {
	case req.CallbackData != "":
		cbID := strconv.FormatInt(time.Now().UnixNano(), 10)
		upd.CallbackQuery = &CallbackQuery{
			ID:           cbID,
			From:         from,
			ChatInstance: strconv.FormatInt(req.ChatID, 10),
			Data:         req.CallbackData,
			Message: &Message{
				MessageID: req.MessageID,
				Chat:      Chat{ID: req.ChatID, Type: "private"},
				Date:      time.Now().Unix(),
			},
		}
		// Register cb_id → chat_id so the bot's eventual
		// answerCallbackQuery(cb_id, text=...) can route the toast back
		// to the correct chat for the debug UI.
		bot.RegisterCallback(cbID, req.ChatID)
	default:
		upd.Message = &Message{
			MessageID: h.Store.NextMessageID(),
			From:      &from,
			Date:      time.Now().Unix(),
			Chat: Chat{
				ID: req.ChatID, Type: "private",
				FirstName: req.FirstName, Username: req.Username,
			},
			Text: req.Text,
		}
	}

	status, err := h.WebhookDispatcher.Deliver(c.Request.Context(), bot, upd)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"ok":              false,
			"update_id":       upd.UpdateID,
			"delivery_status": status,
			"error":           err.Error(),
		})
		return
	}
	resp := gin.H{
		"ok":              true,
		"update_id":       upd.UpdateID,
		"delivery_status": status,
	}
	if upd.CallbackQuery != nil {
		resp["callback_query_id"] = upd.CallbackQuery.ID
	}
	c.JSON(http.StatusOK, resp)
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
