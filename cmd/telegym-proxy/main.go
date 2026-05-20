// Command telegym-proxy bridges a real Telegram chat to the telegym mock so a
// developer can test a bot-under-test using a real Telegram client UI.
//
// Setup:
//  1. Create a throwaway bot via @BotFather, copy its token → PROXY_TOKEN
//  2. Run your bot-under-test against the mock as usual (TELEGRAM_API_URL
//     pointed at telegym-mock, any test token e.g. 1234567890:telegym_default_mock_token_xxxxxxxx)
//  3. Run telegym-proxy with PROXY_TOKEN + MOCK_BOT_TOKEN (the bot-under-test's
//     token) + MOCK_URL. Optionally restrict ALLOWED_USER_ID.
//  4. Open the proxy bot in your Telegram client and chat normally.
//
// Architecture:
//   - Talks to REAL Telegram via mymmrac/telego - same SDK the bot-under-test
//     uses, so multipart upload, retries, and type marshaling are off the
//     shelf rather than hand-rolled.
//   - Talks to the MOCK via plain HTTP because the mock's debug API
//     (/debug/inject/update, /debug/files/...) isn't part of the Bot API.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"
)

// ----- config -------------------------------------------------------------

type config struct {
	proxyToken    string // real Bot API token from @BotFather
	mockURL       string // http://localhost:5678
	mockBotToken  string // token bot-under-test uses; matches what xk6 sends
	listen        string // address telegym-proxy listens on for mock /forward callbacks
	publicURL     string // URL the mock should POST forwards to
	allowedUserID int64  // 0 = anyone may use the proxy
	pollTimeout   int    // long-poll timeout for getUpdates
}

func loadConfig() config {
	c := config{
		proxyToken:   envOr("PROXY_TOKEN", ""),
		mockURL:      envOr("MOCK_URL", "http://localhost:5678"),
		mockBotToken: envOr("MOCK_BOT_TOKEN", "1234567890:telegym_default_mock_token_xxxxxxxx"),
		listen:       envOr("TELEGYM_PROXY_LISTEN", ":8090"),
		publicURL:    envOr("TELEGYM_PROXY_PUBLIC_URL", ""),
		pollTimeout:  30,
	}
	if v := os.Getenv("ALLOWED_USER_ID"); v != "" {
		c.allowedUserID, _ = strconv.ParseInt(v, 10, 64)
	}
	flag.StringVar(&c.proxyToken, "proxy-token", c.proxyToken, "real Bot API token (from @BotFather)")
	flag.StringVar(&c.mockURL, "mock-url", c.mockURL, "telegym-mock URL")
	flag.StringVar(&c.mockBotToken, "mock-bot-token", c.mockBotToken, "token the bot-under-test uses on the mock")
	flag.StringVar(&c.listen, "listen", c.listen, "address to serve /forward callbacks on")
	flag.StringVar(&c.publicURL, "public-url", c.publicURL, "URL the mock should POST forwards to (default: http://localhost<listen>)")
	flag.Int64Var(&c.allowedUserID, "allowed-user-id", c.allowedUserID, "0 = allow anyone, otherwise restrict to one Telegram user")
	flag.Parse()

	if c.proxyToken == "" {
		log.Fatal("PROXY_TOKEN is required (from @BotFather)")
	}
	if c.publicURL == "" {
		c.publicURL = "http://localhost" + c.listen
	}
	return c
}

func envOr(k, v string) string {
	if x := os.Getenv(k); x != "" {
		return x
	}
	return v
}

// ----- main ---------------------------------------------------------------

func main() {
	cfg := loadConfig()

	bot, err := telego.NewBot(cfg.proxyToken)
	if err != nil {
		log.Fatalf("proxy: telego.NewBot: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	me, err := bot.GetMe(ctx)
	if err != nil {
		log.Fatalf("proxy: getMe on real Telegram failed: %v", err)
	}
	log.Printf("proxy: connected as @%s (id=%d)", me.Username, me.ID)
	log.Printf("proxy: relaying to mock at %s (bot token %s)", cfg.mockURL, shortToken(cfg.mockBotToken))
	if cfg.allowedUserID > 0 {
		log.Printf("proxy: restricting to user_id=%d", cfg.allowedUserID)
	} else {
		log.Printf("proxy: open to any Telegram user - set ALLOWED_USER_ID to restrict")
	}

	// Drop any leftover webhook on the real bot (getUpdates returns 409
	// conflict otherwise) and clear pending updates so we start clean.
	if err := bot.DeleteWebhook(ctx, &telego.DeleteWebhookParams{DropPendingUpdates: true}); err != nil {
		log.Printf("proxy: DeleteWebhook (best effort): %v", err)
	}

	p := &proxy{
		cfg:        cfg,
		bot:        bot,
		httpc:      &http.Client{Timeout: 60 * time.Second},
		messageMap: map[int]int{},
	}

	// HTTP server for /forward callbacks from the mock.
	mux := http.NewServeMux()
	mux.HandleFunc("/forward", p.handleForward)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	srv := &http.Server{Addr: cfg.listen, Handler: mux}

	go func() {
		log.Printf("proxy: listening on %s", cfg.listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("proxy: listen: %v", err)
		}
	}()

	p.pollLoop(ctx)

	// Graceful shutdown when SIGINT/SIGTERM arrives.
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	log.Printf("proxy: bye")
}

// ----- proxy --------------------------------------------------------------

type proxy struct {
	cfg   config
	bot   *telego.Bot
	httpc *http.Client

	mu         sync.Mutex
	messageMap map[int]int // mock_message_id → real Telegram message_id
}

func (p *proxy) pollLoop(ctx context.Context) {
	offset := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		updates, err := p.bot.GetUpdates(ctx, &telego.GetUpdatesParams{
			Offset:         offset,
			Timeout:        p.cfg.pollTimeout,
			AllowedUpdates: []string{"message", "callback_query"},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("getUpdates: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			p.handleUpdate(ctx, u)
		}
	}
}

func (p *proxy) handleUpdate(ctx context.Context, u telego.Update) {
	switch {
	case u.Message != nil:
		from := u.Message.From
		if from == nil || !p.allowed(from.ID) {
			return
		}
		p.bindIfNeeded(u.Message.Chat.ID)
		p.inject(map[string]any{
			"token":      p.cfg.mockBotToken,
			"user_id":    from.ID,
			"chat_id":    u.Message.Chat.ID,
			"username":   from.Username,
			"first_name": from.FirstName,
			"text":       u.Message.Text,
		})

	case u.CallbackQuery != nil:
		cq := u.CallbackQuery
		if !p.allowed(cq.From.ID) {
			return
		}
		var chatID int64
		mockMsgID := 0
		if cq.Message != nil {
			chatID = cq.Message.GetChat().ID
			mockMsgID = p.lookupMockID(cq.Message.GetMessageID())
		}
		p.bindIfNeeded(chatID)
		p.inject(map[string]any{
			"token":         p.cfg.mockBotToken,
			"user_id":       cq.From.ID,
			"chat_id":       chatID,
			"username":      cq.From.Username,
			"first_name":    cq.From.FirstName,
			"callback_data": cq.Data,
			"message_id":    mockMsgID,
		})
		// Always answer the callback so the spinner clears on the real client.
		_ = p.bot.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{
			CallbackQueryID: cq.ID,
		})
	}
}

func (p *proxy) allowed(userID int64) bool {
	return p.cfg.allowedUserID == 0 || p.cfg.allowedUserID == userID
}

// bindIfNeeded tells the mock "any outbound for chatID should POST to me".
// Register is idempotent on the mock side, so we just call it on every
// inbound - handles the case where the mock restarts mid-session.
func (p *proxy) bindIfNeeded(chatID int64) {
	if chatID == 0 {
		return
	}
	body, _ := json.Marshal(map[string]any{
		"token":       p.cfg.mockBotToken,
		"webhook_url": p.cfg.publicURL + "/forward",
		"chat_ids":    []int64{chatID},
	})
	resp, err := p.httpc.Post(p.cfg.mockURL+"/debug/proxy/register", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("proxy: register: %v", err)
		return
	}
	_ = resp.Body.Close()
}

func (p *proxy) inject(payload map[string]any) {
	body, _ := json.Marshal(payload)
	resp, err := p.httpc.Post(p.cfg.mockURL+"/debug/inject/update", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("proxy: inject: %v", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		log.Printf("proxy: inject HTTP %d: %s", resp.StatusCode, b)
	}
}

// handleForward is the mock → proxy webhook: receives an outbound Message the
// bot-under-test produced for one of the registered chat_ids.
//
// Two-pass parse: first into telego.Message for the standard fields, then
// into our own struct to recover the FULL reply_markup union (reply
// keyboards, remove_keyboard, etc.) - telego.Message.ReplyMarkup is
// strictly *InlineKeyboardMarkup and silently drops everything else.
func (p *proxy) handleForward(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var std struct {
		Token   string         `json:"token"`
		Message telego.Message `json:"message"`
	}
	if err := json.Unmarshal(body, &std); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var ext struct {
		Message struct {
			ReplyMarkup *incomingMarkup `json:"reply_markup"`
		} `json:"message"`
	}
	_ = json.Unmarshal(body, &ext) // best-effort

	markup := buildMarkup(ext.Message.ReplyMarkup)
	if err := p.forwardToReal(r.Context(), std.Message, markup); err != nil {
		log.Printf("proxy: forward: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// incomingMarkup mirrors the mock's ReplyMarkup union shape so the proxy
// can recover the full intent of the bot's reply_markup field. telego's
// own struct is inline-only and would silently drop reply keyboards /
// remove_keyboard / force_reply.
type incomingMarkup struct {
	InlineKeyboard [][]telego.InlineKeyboardButton `json:"inline_keyboard,omitempty"`

	Keyboard              [][]incomingKbdButton `json:"keyboard,omitempty"`
	ResizeKeyboard        bool                  `json:"resize_keyboard,omitempty"`
	OneTimeKeyboard       bool                  `json:"one_time_keyboard,omitempty"`
	IsPersistent          bool                  `json:"is_persistent,omitempty"`
	InputFieldPlaceholder string                `json:"input_field_placeholder,omitempty"`
	Selective             bool                  `json:"selective,omitempty"`

	RemoveKeyboard bool `json:"remove_keyboard,omitempty"`
	ForceReply     bool `json:"force_reply,omitempty"`
}

type incomingKbdButton struct {
	Text            string `json:"text"`
	RequestContact  bool   `json:"request_contact,omitempty"`
	RequestLocation bool   `json:"request_location,omitempty"`
}

// buildMarkup picks the right telego type for whichever shape the bot used.
// Priority: remove > force_reply > reply_keyboard > inline_keyboard > nil.
// Returns a TRUE nil interface for the no-markup case (avoids the typed-nil
// trap that previously caused the API to reject {"inline_keyboard":null}).
func buildMarkup(in *incomingMarkup) telego.ReplyMarkup {
	if in == nil {
		return nil
	}
	switch {
	case in.RemoveKeyboard:
		return &telego.ReplyKeyboardRemove{
			RemoveKeyboard: true,
			Selective:      in.Selective,
		}
	case in.ForceReply:
		return &telego.ForceReply{
			ForceReply:            true,
			InputFieldPlaceholder: in.InputFieldPlaceholder,
			Selective:             in.Selective,
		}
	case len(in.Keyboard) > 0:
		rows := make([][]telego.KeyboardButton, 0, len(in.Keyboard))
		for _, row := range in.Keyboard {
			out := make([]telego.KeyboardButton, 0, len(row))
			for _, b := range row {
				out = append(out, telego.KeyboardButton{
					Text:            b.Text,
					RequestContact:  b.RequestContact,
					RequestLocation: b.RequestLocation,
				})
			}
			rows = append(rows, out)
		}
		return &telego.ReplyKeyboardMarkup{
			Keyboard:              rows,
			ResizeKeyboard:        in.ResizeKeyboard,
			OneTimeKeyboard:       in.OneTimeKeyboard,
			IsPersistent:          in.IsPersistent,
			InputFieldPlaceholder: in.InputFieldPlaceholder,
			Selective:             in.Selective,
		}
	case len(in.InlineKeyboard) > 0:
		return &telego.InlineKeyboardMarkup{InlineKeyboard: in.InlineKeyboard}
	}
	return nil
}

// forwardToReal translates an outbound Message from the bot-under-test into
// the appropriate Bot API call against the real proxy bot.
//
// markup is built upstream from the raw payload so reply keyboards
// (which telego.Message.ReplyMarkup can't represent) flow through.
func (p *proxy) forwardToReal(ctx context.Context, m telego.Message, markup telego.ReplyMarkup) error {
	chatID := tu.ID(m.Chat.ID)

	switch {
	case len(m.Photo) > 0:
		return p.relayMedia(ctx, m, "photo", m.Photo[0].FileID, m.Caption, markup)
	case m.Video != nil:
		return p.relayMedia(ctx, m, "video", m.Video.FileID, m.Caption, markup)
	case m.Animation != nil:
		return p.relayMedia(ctx, m, "animation", m.Animation.FileID, m.Caption, markup)
	case m.Sticker != nil:
		// Stickers from the bot are file_id references we don't own; fall
		// back to text so the developer at least sees something landed.
		text := "[sticker]"
		if m.Caption != "" {
			text = "[sticker: " + m.Caption + "]"
		}
		sent, err := p.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: chatID, Text: text, ReplyMarkup: markup,
		})
		return p.recordSent(m.MessageID, sent, err)
	case m.Dice != nil:
		sent, err := p.bot.SendDice(ctx, &telego.SendDiceParams{
			ChatID: chatID, Emoji: m.Dice.Emoji,
		})
		return p.recordSent(m.MessageID, sent, err)
	default:
		text := m.Text
		if text == "" {
			text = m.Caption
		}
		if text == "" {
			return nil // nothing meaningful to forward
		}
		sent, err := p.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: chatID, Text: text, ReplyMarkup: markup,
		})
		return p.recordSent(m.MessageID, sent, err)
	}
}

// relayMedia downloads the file bytes from the mock's file store and uploads
// them to real Telegram as a fresh InputFile. The proxy bot's file_id pool
// is private to it, so we can't simply pass the bot-under-test's file_ids
// through.
func (p *proxy) relayMedia(ctx context.Context, m telego.Message, kind, fileID, caption string, markup telego.ReplyMarkup) error {
	data, ct, filename, err := p.fetchFile(fileID)
	if err != nil {
		// File not available - surface as text so the dev sees something.
		text := fmt.Sprintf("[%s unavailable: %s]", kind, fileID)
		if caption != "" {
			text = caption + "\n\n" + text
		}
		sent, sendErr := p.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID: tu.ID(m.Chat.ID), Text: text, ReplyMarkup: markup,
		})
		return p.recordSent(m.MessageID, sent, sendErr)
	}
	if filename == "" {
		filename = kind + ".bin"
	}
	_ = ct // Telegram sniffs content type from bytes; tu has no slot for it here.

	input := telego.InputFile{File: tu.NameReader(bytes.NewReader(data), filename)}

	var sent *telego.Message
	var sendErr error
	switch kind {
	case "photo":
		sent, sendErr = p.bot.SendPhoto(ctx, &telego.SendPhotoParams{
			ChatID: tu.ID(m.Chat.ID), Photo: input, Caption: caption, ReplyMarkup: markup,
		})
	case "video":
		sent, sendErr = p.bot.SendVideo(ctx, &telego.SendVideoParams{
			ChatID: tu.ID(m.Chat.ID), Video: input, Caption: caption, ReplyMarkup: markup,
		})
	case "animation":
		sent, sendErr = p.bot.SendAnimation(ctx, &telego.SendAnimationParams{
			ChatID: tu.ID(m.Chat.ID), Animation: input, Caption: caption, ReplyMarkup: markup,
		})
	default:
		return fmt.Errorf("relayMedia: unsupported kind %q", kind)
	}
	return p.recordSent(m.MessageID, sent, sendErr)
}

func (p *proxy) fetchFile(fileID string) ([]byte, string, string, error) {
	resp, err := p.httpc.Get(p.cfg.mockURL + "/debug/files/" + fileID)
	if err != nil {
		return nil, "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("file %s: HTTP %d", fileID, resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", "", err
	}
	ct := resp.Header.Get("Content-Type")

	// The mock sets Content-Disposition: inline; filename="..."
	filename := ""
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		const key = `filename="`
		if i := indexOf(cd, key); i >= 0 {
			rest := cd[i+len(key):]
			if j := indexOf(rest, `"`); j >= 0 {
				filename = rest[:j]
			}
		}
	}
	return b, ct, filename, nil
}

// recordSent stores the mock→real message_id mapping (so the bot's edits
// land on the right real message) and propagates the send error.
func (p *proxy) recordSent(mockID int, sent *telego.Message, err error) error {
	if err != nil {
		return err
	}
	if sent != nil {
		p.remember(mockID, sent.MessageID)
	}
	return nil
}

func (p *proxy) remember(mockID, realID int) {
	if mockID == 0 || realID == 0 {
		return
	}
	p.mu.Lock()
	p.messageMap[realID] = mockID
	p.mu.Unlock()
}

func (p *proxy) lookupMockID(realID int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.messageMap[realID]
}

func shortToken(t string) string {
	if len(t) <= 10 {
		return t
	}
	return t[:6] + "…"
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
