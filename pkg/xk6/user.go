package telegym

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"

	"go.k6.io/k6/v2/js/modules"
)

// User is the JS-facing handle for a virtual Telegram user. Every send/click
// goes through the mock's /debug/inject/update endpoint; every await polls
// /debug/messages until a match is found.
type User struct {
	client *Client
	vu     modules.VU

	ChatID    int64  `js:"chatId"`
	UserID    int64  `js:"userId"`
	Username  string `js:"username"`
	FirstName string `js:"firstName"`

	since            int64 // unix seconds floor for polling
	lastSeenEventSeq int64 // dedupes by mock-internal event sequence
	// (handles bot's editMessage where two entries
	// share MessageID but differ in EventSeq)
	lastSeenMessageID int // last MATCHED telegram message_id - used as
	// the target message for Click() callbacks

	requestsFrom   int64               // unix seconds floor for journal entries
	seenRequestSeq map[string]int64    // last matched journal seq per method
	queryIDs       map[string]struct{} // callback, inline and pre-checkout queries this user injected
}

// Message is what awaitText / awaitButton return. LatencyMS is set by the
// await method so scenarios can assert reply timings via k6 `check()`.
type Message struct {
	MessageID   int          `json:"message_id" js:"messageId"`
	Text        string       `json:"text"`
	Caption     string       `json:"caption"`
	Date        int64        `json:"date"`
	ReplyMarkup *ReplyMarkup `json:"reply_markup,omitempty" js:"replyMarkup"`
	Widget      *Widget      `json:"widget,omitempty"`

	// EventSeq is the mock-internal monotonic counter - distinct on every
	// AppendMessage even when MessageID collides (editMessage*). Used to
	// dedupe matches across consecutive awaits without missing edits.
	EventSeq int64 `json:"_event_seq,omitempty"`

	LatencyMS float64 `json:"-" js:"latencyMs"`
	user      *User   `json:"-"`
}

// Widget is the part of Message.widget scenarios assert on.
type Widget struct {
	ID         string `json:"id,omitempty"`
	PublicData string `json:"public_data,omitempty" js:"publicData"`
	ChatData   string `json:"chat_data,omitempty" js:"chatData"`
}

type ReplyMarkup struct {
	InlineKeyboard [][]Button `json:"inline_keyboard" js:"inlineKeyboard"`
}

type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data" js:"callbackData"`
	URL          string `json:"url,omitempty"`
}

// APIRequest is one Bot API call from the mock's request journal. LatencyMS
// is set by awaitRequest.
type APIRequest struct {
	Seq        int64          `json:"seq"`
	Method     string         `json:"method"`
	Parameters map[string]any `json:"parameters"`
	Status     int            `json:"status"`
	At         int64          `json:"at"`

	LatencyMS float64 `json:"-" js:"latencyMs"`
}

// Send injects a plain text message from the user.
func (u *User) Send(text string) error {
	return u.inject(map[string]any{
		"token":      u.client.token,
		"user_id":    u.UserID,
		"chat_id":    u.ChatID,
		"username":   u.Username,
		"first_name": u.FirstName,
		"text":       text,
	})
}

// Click injects a callback query for the given callback_data, attached to the
// most recently matched bot message.
func (u *User) Click(callbackData string) error {
	return u.inject(map[string]any{
		"token":         u.client.token,
		"user_id":       u.UserID,
		"chat_id":       u.ChatID,
		"username":      u.Username,
		"first_name":    u.FirstName,
		"callback_data": callbackData,
		"message_id":    u.lastSeenMessageID,
	})
}

// BlockBot injects the my_chat_member update of the user blocking the bot.
func (u *User) BlockBot() error {
	_, err := u.injectAs("my_chat_member", map[string]any{"old_status": "member", "new_status": "kicked"})
	return err
}

// UnblockBot injects the my_chat_member update of the user unblocking the bot.
func (u *User) UnblockBot() error {
	_, err := u.injectAs("my_chat_member", map[string]any{"old_status": "kicked", "new_status": "member"})
	return err
}

// InlineQuery injects an inline query and returns its id.
func (u *User) InlineQuery(query string) (string, error) {
	return u.injectAs("inline_query", map[string]any{"query": query})
}

// ChooseInlineResult injects the user picking resultID for query.
func (u *User) ChooseInlineResult(resultID, query string) error {
	_, err := u.injectAs("chosen_inline_result", map[string]any{"result_id": resultID, "query": query})
	return err
}

// PreCheckout injects a pre-checkout query and returns its id. An empty
// currency means Telegram Stars.
func (u *User) PreCheckout(payload string, amount int, currency string) (string, error) {
	return u.injectAs("pre_checkout_query", paymentFields(payload, amount, currency))
}

// Pay injects a successful_payment message. An empty currency means Telegram
// Stars.
func (u *User) Pay(payload string, amount int, currency string) error {
	_, err := u.injectAs("successful_payment", paymentFields(payload, amount, currency))
	return err
}

// React injects an emoji reaction on the most recently matched bot message.
func (u *User) React(emoji string) error {
	_, err := u.injectAs("message_reaction", map[string]any{
		"message_id":   u.lastSeenMessageID,
		"new_reaction": []string{emoji},
	})
	return err
}

// AnswerPoll injects the user's vote. No options retracts it.
func (u *User) AnswerPoll(pollID string, optionIDs []int) error {
	_, err := u.injectAs("poll_answer", map[string]any{"poll_id": pollID, "option_ids": optionIDs})
	return err
}

// TapWidget injects a widget query with data from the widget in the most
// recently matched bot message and returns its id.
func (u *User) TapWidget(data string) (string, error) {
	return u.injectAs("widget_query", map[string]any{"message_id": u.lastSeenMessageID, "widget_data": data})
}

// TapSharedWidget injects a shared widget query with data and returns its id.
func (u *User) TapSharedWidget(widgetID, data string) (string, error) {
	return u.injectAs("shared_widget_query", map[string]any{"widget_id": widgetID, "widget_data": data})
}

// SendPhoto injects a photo message referencing fileID.
func (u *User) SendPhoto(fileID, caption string) error {
	_, err := u.injectAs("message", map[string]any{
		"photo":   []map[string]any{{"file_id": fileID}},
		"caption": caption,
	})
	return err
}

// SendDocument injects a document message referencing fileID.
func (u *User) SendDocument(fileID, fileName, caption string) error {
	_, err := u.injectAs("message", map[string]any{
		"document": map[string]any{"file_id": fileID, "file_name": fileName},
		"caption":  caption,
	})
	return err
}

// SendContact injects the user sharing their own phone number.
func (u *User) SendContact(phone string) error {
	_, err := u.injectAs("message", map[string]any{
		"contact": map[string]any{"phone_number": phone, "user_id": u.UserID},
	})
	return err
}

// SendLocation injects a location message.
func (u *User) SendLocation(latitude, longitude float64) error {
	_, err := u.injectAs("message", map[string]any{
		"location": map[string]any{"latitude": latitude, "longitude": longitude},
	})
	return err
}

// SendSticker injects a sticker message referencing fileID.
func (u *User) SendSticker(fileID, emoji string) error {
	_, err := u.injectAs("message", map[string]any{
		"sticker": map[string]any{"file_id": fileID, "emoji": emoji},
	})
	return err
}

// AwaitText blocks until a bot message arrives whose text (or caption) matches
// pattern (Go regexp). Returns error on timeout. timeoutSec is fractional.
func (u *User) AwaitText(pattern string, timeoutSec float64) (*Message, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %w", pattern, err)
	}
	return u.await(timeoutSec, func(m Message) bool {
		return re.MatchString(m.Text) || re.MatchString(m.Caption)
	})
}

// AwaitButton blocks until a bot message arrives whose inline keyboard
// contains a button with callback_data matching pattern.
func (u *User) AwaitButton(pattern string, timeoutSec float64) (*Message, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %w", pattern, err)
	}
	return u.await(timeoutSec, func(m Message) bool {
		if m.ReplyMarkup == nil {
			return false
		}
		for _, row := range m.ReplyMarkup.InlineKeyboard {
			for _, b := range row {
				if re.MatchString(b.CallbackData) {
					return true
				}
			}
		}
		return false
	})
}

// AwaitRequest blocks until the bot calls method with parameters that include
// params, and returns that call. The journal is shared by all VUs, so calls
// carrying another user's chat_id or query id never match. Each call is
// returned once per user, in order within a method.
func (u *User) AwaitRequest(method string, params map[string]any, timeoutSec float64) (*APIRequest, error) {
	start := time.Now()
	deadline := start.Add(time.Duration(timeoutSec * float64(time.Second)))
	poll := 25 * time.Millisecond
	const maxPoll = 250 * time.Millisecond

	for {
		requests, err := u.client.requests(method, awaitRequestWindow)
		if err != nil {
			return nil, err
		}
		for i := range requests {
			r := &requests[i]
			if r.Seq <= u.seenRequestSeq[method] || r.At < u.requestsFrom ||
				!u.ownsRequest(r) || !includesParams(r.Parameters, params) {
				continue
			}
			u.seenRequestSeq[method] = r.Seq
			r.LatencyMS = float64(time.Since(start).Microseconds()) / 1000.0
			return r, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("await request timeout after %.1fs (chat=%d, method=%s, seen_seq=%d)",
				timeoutSec, u.ChatID, method, u.seenRequestSeq[method])
		}
		time.Sleep(poll)
		if poll < maxPoll {
			poll *= 2
		}
	}
}

// FindButton scans a Message's inline keyboard for a callback_data matching
// pattern. Returns nil if not found. Useful when the scenario needs to extract
// a specific button (e.g. "country:UA" from a list of countries).
func (m *Message) FindButton(pattern string) *Button {
	if m == nil || m.ReplyMarkup == nil {
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	for _, row := range m.ReplyMarkup.InlineKeyboard {
		for _, b := range row {
			if re.MatchString(b.CallbackData) {
				cb := b
				return &cb
			}
		}
	}
	return nil
}

// Click on a Button object directly. Convenience for: u.awaitButton(...).findButton(...).click()
func (b *Button) Click(u *User) error {
	if b == nil {
		return fmt.Errorf("nil button")
	}
	return u.Click(b.CallbackData)
}

// ===================================================================
// internals
// ===================================================================

func (u *User) inject(body map[string]any) error {
	_, err := u.post(body)
	return err
}

// injectAs injects an update of the given type from this user and returns
// the query id the mock assigned, if any.
func (u *User) injectAs(updateType string, fields map[string]any) (string, error) {
	fields["token"] = u.client.token
	fields["type"] = updateType
	fields["user_id"] = u.UserID
	fields["chat_id"] = u.ChatID
	fields["username"] = u.Username
	fields["first_name"] = u.FirstName
	return u.post(fields)
}

func paymentFields(payload string, amount int, currency string) map[string]any {
	return map[string]any{"invoice_payload": payload, "total_amount": amount, "currency": currency}
}

func (u *User) post(body map[string]any) (string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	resp, err := u.client.http.Post(
		u.client.mockURL+"/debug/inject/update",
		"application/json",
		bytes.NewReader(b),
	)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("inject: %d %s", resp.StatusCode, body)
	}
	var out struct {
		CallbackQueryID    string `json:"callback_query_id"`
		InlineQueryID      string `json:"inline_query_id"`
		PreCheckoutQueryID string `json:"pre_checkout_query_id"`
		WidgetQueryID      string `json:"widget_query_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	id := cmp.Or(out.CallbackQueryID, out.InlineQueryID, out.PreCheckoutQueryID, out.WidgetQueryID)
	if id != "" {
		u.queryIDs[id] = struct{}{}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return id, nil
}

// awaitRequestWindow is how many of a method's newest journal entries each
// awaitRequest poll reads: enough to survive busy runs, small enough that
// polling stays cheap.
const awaitRequestWindow = 1000

// queryIDParams are the Bot API parameters that answer a query a user injected.
var queryIDParams = []string{"callback_query_id", "inline_query_id", "pre_checkout_query_id", "widget_query_id"}

// ownsRequest reports whether a journal entry can belong to this user. Calls
// tied to neither a chat nor a query, such as getMe, match every user.
func (u *User) ownsRequest(r *APIRequest) bool {
	if id, ok := r.Parameters["chat_id"]; ok {
		return paramText(id) == strconv.FormatInt(u.ChatID, 10)
	}
	for _, param := range queryIDParams {
		if id, ok := r.Parameters[param]; ok {
			_, injected := u.queryIDs[paramText(id)]
			return injected
		}
	}
	return true
}

func includesParams(got, want map[string]any) bool {
	for key, value := range want {
		actual, ok := got[key]
		if !ok || paramText(actual) != paramText(value) {
			return false
		}
	}
	return true
}

// paramText normalizes a parameter for comparison: bots may send the same
// value as a JSON number or as a form-encoded string.
func paramText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

type messagesResp struct {
	Messages []Message `json:"messages"`
}

// await polls /debug/messages with exponential-ish backoff until match or
// timeout. Records latency on the returned Message so scenarios can assert.
func (u *User) await(timeoutSec float64, match func(Message) bool) (*Message, error) {
	start := time.Now()
	deadline := start.Add(time.Duration(timeoutSec * float64(time.Second)))
	poll := 25 * time.Millisecond
	const maxPoll = 250 * time.Millisecond

	for {
		msgs, err := u.fetch()
		if err != nil {
			return nil, err
		}
		for _, m := range msgs {
			// Dedupe by EventSeq, not MessageID - handles bot edits where
			// two store entries share MessageID but the edit gets a higher
			// EventSeq from the mock's monotonic counter.
			if m.EventSeq != 0 && m.EventSeq <= u.lastSeenEventSeq {
				continue
			}
			if match(m) {
				u.lastSeenEventSeq = m.EventSeq
				u.lastSeenMessageID = m.MessageID
				u.since = m.Date
				m.LatencyMS = float64(time.Since(start).Microseconds()) / 1000.0
				m.user = u
				return &m, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("await timeout after %.1fs (chat=%d, seen_seq=%d)",
				timeoutSec, u.ChatID, u.lastSeenEventSeq)
		}
		time.Sleep(poll)
		if poll < maxPoll {
			poll *= 2
		}
	}
}

func (u *User) fetch() ([]Message, error) {
	url := fmt.Sprintf("%s/debug/messages/%s?chat_id=%d&since=%d",
		u.client.mockURL, u.client.token, u.ChatID, u.since)
	resp, err := u.client.http.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var r messagesResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return r.Messages, nil
}
