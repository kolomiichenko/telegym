package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimit is a token bucket: Burst messages at once, then refilling at
// Rate messages per second.
type RateLimit struct {
	Rate, Burst float64
}

// FloodLimits configures the emulated Telegram flood limits. Zero fields
// keep DefaultFloodLimits.
type FloodLimits struct {
	Bot     RateLimit // all chats of one bot
	Paid    RateLimit // one bot's calls with allow_paid_broadcast, instead of Bot
	Private RateLimit // one private chat
	Group   RateLimit // one group or channel
}

// DefaultFloodLimits follows https://core.telegram.org/bots/faq#my-bot-is-hitting-limits-how-do-i-avoid-this
// and allow_paid_broadcast in the Bot API. Telegram tolerates short bursts
// and publishes no exact burst sizes, so those are estimates.
var DefaultFloodLimits = FloodLimits{
	Bot:     RateLimit{Rate: 30, Burst: 30},
	Paid:    RateLimit{Rate: 1000, Burst: 1000},
	Private: RateLimit{Rate: 1, Burst: 3},
	Group:   RateLimit{Rate: 20.0 / 60, Burst: 20},
}

func (f FloodLimits) withDefaults() FloodLimits {
	pick := func(v, def RateLimit) RateLimit {
		if v.Rate <= 0 || v.Burst < 1 {
			return def
		}
		return v
	}
	d := DefaultFloodLimits
	return FloodLimits{
		Bot:     pick(f.Bot, d.Bot),
		Paid:    pick(f.Paid, d.Paid),
		Private: pick(f.Private, d.Private),
		Group:   pick(f.Group, d.Group),
	}
}

// ParseRateLimit parses "N/s", "N/min" or "N/h", optionally followed by
// ",burst". Without a burst, N messages may go out at once: "20/min" allows
// 20 immediately, then one every 3s.
func ParseRateLimit(s string) (RateLimit, error) {
	spec, burstText, hasBurst := strings.Cut(s, ",")
	countText, unit, ok := strings.Cut(strings.TrimSpace(spec), "/")
	if !ok {
		return RateLimit{}, fmt.Errorf("want N/s, N/min or N/h, got %q", s)
	}
	count, err := strconv.ParseFloat(strings.TrimSpace(countText), 64)
	if err != nil || count <= 0 {
		return RateLimit{}, fmt.Errorf("want a positive count, got %q", countText)
	}
	periods := map[string]float64{"s": 1, "min": 60, "h": 3600}
	period, ok := periods[strings.TrimSpace(unit)]
	if !ok {
		return RateLimit{}, fmt.Errorf("want unit s, min or h, got %q", unit)
	}
	limit := RateLimit{Rate: count / period, Burst: max(count, 1)}
	if hasBurst {
		if limit.Burst, err = strconv.ParseFloat(strings.TrimSpace(burstText), 64); err != nil || limit.Burst < 1 {
			return RateLimit{}, fmt.Errorf("want burst >= 1, got %q", burstText)
		}
	}
	return limit, nil
}

// limitSweepInterval bounds how often idle buckets are dropped, so long runs
// over many chats keep the limiter small.
const limitSweepInterval = time.Minute

type bucket struct {
	tokens float64
	last   time.Time
}

// refill tops the bucket up for the time elapsed since its last use.
func (b *bucket) refill(now time.Time, rate, burst float64) {
	if b.last.IsZero() {
		b.tokens = burst
	} else {
		b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	}
	b.last = now
}

// wait returns how long until the bucket holds a whole token.
func (b *bucket) wait(rate float64) time.Duration {
	if b.tokens >= 1 {
		return 0
	}
	return time.Duration((1 - b.tokens) / rate * float64(time.Second))
}

// rateLimiter emulates Telegram's per-bot and per-chat flood limits. It
// exists even while disabled, so /debug/ratelimits can turn it on mid-run.
type rateLimiter struct {
	enabled   atomic.Bool
	mu        sync.Mutex
	now       func() time.Time
	limits    FloodLimits
	bots      map[string]*bucket
	paid      map[string]*bucket
	chats     map[string]*bucket // keyed by token + "\x00" + chat_id
	lastSweep time.Time
}

func newRateLimiter(limits FloodLimits) *rateLimiter {
	return &rateLimiter{
		now:    time.Now,
		limits: limits.withDefaults(),
		bots:   make(map[string]*bucket),
		paid:   make(map[string]*bucket),
		chats:  make(map[string]*bucket),
	}
}

// allow takes a token from both the bot and chat buckets, or returns how
// long the caller must wait when either is empty. Nothing is consumed on
// refusal, matching Telegram where rejected calls do not count. Paid
// broadcasts draw on their own, larger bot bucket; chat limits still apply.
func (l *rateLimiter) allow(token, chatID string, paid bool) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	botLimit, bots := l.limits.Bot, l.bots
	if paid {
		botLimit, bots = l.limits.Paid, l.paid
	}
	chatLimit := l.limits.Private
	if strings.HasPrefix(chatID, "-") || strings.HasPrefix(chatID, "@") {
		chatLimit = l.limits.Group
	}
	bot := l.bucket(bots, token)
	chat := l.bucket(l.chats, token+"\x00"+chatID)
	bot.refill(now, botLimit.Rate, botLimit.Burst)
	chat.refill(now, chatLimit.Rate, chatLimit.Burst)

	if wait := max(bot.wait(botLimit.Rate), chat.wait(chatLimit.Rate)); wait > 0 {
		return wait
	}
	bot.tokens--
	chat.tokens--
	return 0
}

func (l *rateLimiter) bucket(m map[string]*bucket, key string) *bucket {
	b, ok := m[key]
	if !ok {
		b = &bucket{}
		m[key] = b
	}
	return b
}

// sweep drops buckets idle long enough to have refilled completely; a
// fresh bucket starts full, so forgetting them changes nothing.
func (l *rateLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < limitSweepInterval {
		return
	}
	l.lastSweep = now
	for _, m := range []map[string]*bucket{l.bots, l.paid, l.chats} {
		for key, b := range m {
			if now.Sub(b.last) >= limitSweepInterval {
				delete(m, key)
			}
		}
	}
}

// rateLimitSettings is the /debug/ratelimits body, limits in the same
// rate[,burst] format as the env vars. Omitted fields are left unchanged.
type rateLimitSettings struct {
	Enabled *bool   `json:"enabled,omitempty"`
	Bot     *string `json:"bot,omitempty"`
	Paid    *string `json:"paid,omitempty"`
	Private *string `json:"private,omitempty"`
	Group   *string `json:"group,omitempty"`
}

func (l *rateLimiter) settings() rateLimitSettings {
	l.mu.Lock()
	defer l.mu.Unlock()

	text := func(limit RateLimit) *string {
		s := formatRateLimit(limit)
		return &s
	}
	enabled := l.enabled.Load()
	return rateLimitSettings{
		Enabled: &enabled,
		Bot:     text(l.limits.Bot),
		Paid:    text(l.limits.Paid),
		Private: text(l.limits.Private),
		Group:   text(l.limits.Group),
	}
}

// apply changes the given settings, or none of them if any is invalid. New
// limits take effect on the next call. Buckets are kept: refill caps them at
// the new burst, so a tightened limit bites at once.
func (l *rateLimiter) apply(in rateLimitSettings) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	limits := l.limits
	for _, field := range []struct {
		name  string
		value *string
		dst   *RateLimit
	}{
		{"bot", in.Bot, &limits.Bot},
		{"paid", in.Paid, &limits.Paid},
		{"private", in.Private, &limits.Private},
		{"group", in.Group, &limits.Group},
	} {
		if field.value == nil {
			continue
		}
		parsed, err := ParseRateLimit(*field.value)
		if err != nil {
			return fmt.Errorf("%s: %w", field.name, err)
		}
		*field.dst = parsed
	}
	l.limits = limits
	if in.Enabled != nil {
		l.enabled.Store(*in.Enabled)
	}
	return nil
}

// formatRateLimit is the inverse of ParseRateLimit, picking the shortest
// unit that gives a whole count: 20/min rather than 0.333/s.
func formatRateLimit(limit RateLimit) string {
	count, unit := limit.Rate, "s"
	for _, u := range []struct {
		name    string
		seconds float64
	}{{"s", 1}, {"min", 60}, {"h", 3600}} {
		if c := limit.Rate * u.seconds; math.Abs(c-math.Round(c)) < 1e-9 {
			count, unit = math.Round(c), u.name
			break
		}
	}
	return strconv.FormatFloat(count, 'f', -1, 64) + "/" + unit + "," + strconv.FormatFloat(limit.Burst, 'f', -1, 64)
}

// debugGetRateLimits serves GET /debug/ratelimits.
func (s *Server) debugGetRateLimits(c *gin.Context) {
	c.JSON(http.StatusOK, s.limiter.settings())
}

// debugPutRateLimits serves PUT /debug/ratelimits. Unknown fields are refused
// so a misspelled limit cannot silently leave the old one in force.
func (s *Server) debugPutRateLimits(c *gin.Context) {
	var in rateLimitSettings
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.limiter.apply(in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s.limiter.settings())
}

// isFloodLimited reports whether a Bot API method sends a message and so
// counts against Telegram's flood limits.
func isFloodLimited(method string) bool {
	switch method {
	case "sendChatAction", "sendMessageDraft", "sendRichMessageDraft":
		return false
	case "copyMessage", "copyMessages", "forwardMessage", "forwardMessages":
		return true
	}
	return strings.HasPrefix(method, "send")
}

// faultsMiddleware adds configured latency, random 500s and Telegram flood
// limits to Bot API calls. Debug and chat UI routes are left untouched.
func (s *Server) faultsMiddleware() gin.HandlerFunc {
	limiter := s.limiter
	return func(c *gin.Context) {
		token, method, ok := parseBotPath(c.Request.URL.Path)
		if !ok {
			c.Next()
			return
		}

		if delay := s.latency(); delay > 0 {
			select {
			case <-time.After(delay):
			case <-c.Request.Context().Done():
				c.Abort()
				return
			}
		}

		if s.cfg.FaultRate > 0 && rand.Float64() < s.cfg.FaultRate {
			c.AbortWithStatusJSON(http.StatusInternalServerError, APIResponse{
				OK: false, ErrorCode: http.StatusInternalServerError, Description: "Internal Server Error",
			})
			return
		}

		if !limiter.enabled.Load() || !isFloodLimited(method) {
			c.Next()
			return
		}
		chatID, paid := floodKey(c)
		if wait := limiter.allow(token, chatID, paid); wait > 0 {
			tooManyRequests(c, wait)
			return
		}
		c.Next()
		if paid && c.Writer.Status() == http.StatusOK {
			s.metrics.PaidBroadcastMessagesTotal.Inc()
		}
	}
}

// tooManyRequests answers a flood with 429 and retry_after in whole
// seconds, rounded up as Telegram does.
func tooManyRequests(c *gin.Context, wait time.Duration) {
	retryAfter := max(1, int(math.Ceil(wait.Seconds())))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, APIResponse{
		OK:          false,
		ErrorCode:   http.StatusTooManyRequests,
		Description: "Too Many Requests: retry after " + strconv.Itoa(retryAfter),
		Parameters:  &ResponseParameters{RetryAfter: retryAfter},
	})
}

// latency picks a delay in [LatencyMin, LatencyMax].
func (s *Server) latency() time.Duration {
	lo, hi := s.cfg.LatencyMin, s.cfg.LatencyMax
	if hi <= lo {
		return lo
	}
	return lo + rand.N(hi-lo+1)
}

// floodKey reads chat_id and allow_paid_broadcast from the query, form or
// JSON body without consuming the body for the handler that runs next.
func floodKey(c *gin.Context) (chatID string, paid bool) {
	ct := c.ContentType()
	if ct == "multipart/form-data" || ct == "application/x-www-form-urlencoded" || c.Request.Body == nil {
		// Parsed forms are cached on the request, so handlers reuse them.
		return c.Request.FormValue("chat_id"), c.Request.FormValue("allow_paid_broadcast") == "true"
	}
	chatID, paid = c.Query("chat_id"), c.Query("allow_paid_broadcast") == "true"
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		// Replay what was read, then the same error, so the handler still
		// reports the broken body.
		c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), errReader{err}))
		return chatID, paid
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	var payload struct {
		ChatID             json.RawMessage `json:"chat_id"`
		AllowPaidBroadcast bool            `json:"allow_paid_broadcast"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if len(payload.ChatID) > 0 {
			chatID = strings.Trim(string(payload.ChatID), `"`)
		}
		paid = paid || payload.AllowPaidBroadcast
	}
	return chatID, paid
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
