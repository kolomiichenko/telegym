package mock

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// The debug chat runs widgets in a sandboxed iframe: the widget page is the
// bot's HTML behind a TelegramWidget runtime, and taps reach the bot as
// widget queries through the chat page.

//go:embed web/widget-runtime.js
var widgetRuntimeJS string

// widgetCSP mirrors the Telegram sandbox: inline code and data: assets only,
// no fetch, XHR or subresource loads. CSP cannot stop the frame navigating
// itself, which the real sandbox does; the mock runs the bot's own widget,
// so that gap only matters for widgets that misbehave. The sandbox directive
// gives the page an opaque origin even when opened directly, outside the
// chat's sandboxed iframe.
const widgetCSP = "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; media-src data:"

// widgetVM places a widget message's iframe in the debug chat.
type widgetVM struct {
	FrameID    string
	URL        string
	QueryURL   string
	Width      int // 0 = bubble width
	Height     int // 0 = sized to the content
	ChatData   string
	PublicData string
}

func newWidgetVM(w *Widget, token string, chatID int64, messageID int, name string) *widgetVM {
	base := fmt.Sprintf("/debug/chat/%s/%d/widget/%d", url.PathEscape(token), chatID, messageID)
	vm := &widgetVM{
		FrameID:    "widget-" + strconv.Itoa(messageID),
		URL:        base + "?" + url.Values{"name": {name}}.Encode(),
		QueryURL:   base + "/query",
		ChatData:   w.ChatData,
		PublicData: w.PublicData,
	}
	// The Telegram app clamps the width to 200 px and up, a fixed height
	// to 50-600 px.
	if w.Width > 0 {
		vm.Width = max(w.Width, 200)
	}
	if w.Height > 0 {
		vm.Height = min(max(w.Height, 50), 600)
	}
	return vm
}

// widgetPage serves the widget HTML behind the TelegramWidget runtime.
func (h *chatHandlers) widgetPage(c *gin.Context) {
	chatID, _ := strconv.ParseInt(c.Param("chat_id"), 10, 64)
	messageID, _ := strconv.Atoi(c.Param("message_id"))
	m, ok := h.store.Bot(c.Param("token")).latestWidgetMessage(chatID, messageID)
	if !ok {
		c.String(http.StatusNotFound, "no widget in message %d", messageID)
		return
	}
	name := c.DefaultQuery("name", "andrii")
	init, _ := json.Marshal(map[string]any{
		"chatData":   nullable(m.Widget.ChatData),
		"publicData": nullable(m.Widget.PublicData),
		"canQuery":   m.Widget.AllowQueries,
		"viewer":     map[string]any{"id": chatID, "first_name": name, "username": name},
	})
	// json.Marshal escapes <, > and &, so init cannot close the script.
	runtime := strings.Replace(widgetRuntimeJS, "/*INIT*/", string(init), 1)

	c.Header("Content-Security-Policy", widgetCSP)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(
		"<!doctype html><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width\"><script>"+
			runtime+"</script>\n"+m.Widget.HTML))
}

// widgetQuery delivers a widget request as a widget_query and waits for the
// bot's answerWidgetQuery. Failures come back as the SDK's request codes.
func (h *chatHandlers) widgetQuery(c *gin.Context) {
	chatID, _ := strconv.ParseInt(c.Param("chat_id"), 10, 64)
	messageID, _ := strconv.Atoi(c.Param("message_id"))
	name := c.DefaultPostForm("name", "andrii")
	bot := h.store.Bot(c.Param("token"))

	from := User{ID: chatID, FirstName: name, Username: name}
	chat := Chat{ID: chatID, Type: "private", FirstName: name, Username: name}
	req := InjectUpdateRequest{Type: "widget_query", MessageID: messageID, WidgetData: c.PostForm("data")}
	var upd Update
	answer, err := fillWidgetQuery(h.store, &upd, bot, &req, from, chat)
	if err != nil {
		code := "NOT_ALLOWED"
		if errors.Is(err, errWidgetQueryFlood) {
			code = "RATE_LIMITED"
		}
		c.JSON(http.StatusOK, gin.H{"code": code, "error": err.Error()})
		return
	}
	if _, err := h.dispatcher.DeliverUpdate(c.Request.Context(), bot, upd); err != nil {
		c.JSON(http.StatusOK, gin.H{"code": "FAILED", "error": err.Error()})
		return
	}

	timeout := time.NewTimer(widgetQueryTTL)
	defer timeout.Stop()
	select {
	case a := <-answer:
		c.JSON(http.StatusOK, a)
	case <-timeout.C:
		c.JSON(http.StatusOK, gin.H{"code": "TIMEOUT"})
	case <-c.Request.Context().Done():
	}
}

// nullable maps absent widget data to null, as the runtime reports it.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
