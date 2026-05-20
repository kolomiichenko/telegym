package mock

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Config configures the mock server.
type Config struct {
	Listen            string // ":5678"
	MetricsListen     string // ":9104" - empty disables
	DefaultWebhookURL string // fallback when bot never called setWebhook
	DefaultToken      string // pre-fills the debug chat home form (no auth, cosmetic)
	Quiet             bool   // suppress request logs (recommended for load runs)
}

// Server is the Bot API mock HTTP server.
type Server struct {
	cfg      Config
	store    *Store
	handlers *Handlers
	chat     *chatHandlers
	metrics  *Metrics
	router   *gin.Engine
}

// New creates a configured Server. Routes are registered immediately so the
// returned Server is ready for ListenAndServe or for use as an http.Handler
// in tests.
func New(cfg Config) *Server {
	store := NewStore()
	disp := NewWebhookDispatcher(cfg.DefaultWebhookURL)
	h := &Handlers{Store: store, WebhookDispatcher: disp}

	if !cfg.Quiet {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(gin.Recovery())
	if !cfg.Quiet {
		router.Use(gin.Logger())
	}

	metrics := NewMetrics(store)
	disp.metrics = metrics
	store.Proxy.metrics = metrics

	s := &Server{
		cfg:      cfg,
		store:    store,
		handlers: h,
		chat:     newChatHandlers(store, disp),
		metrics:  metrics,
		router:   router,
	}
	router.Use(metrics.ginMiddleware())
	s.registerRoutes()
	return s
}

// Handler returns the underlying http.Handler - useful for httptest in unit
// tests of either the mock itself or bots that embed it.
func (s *Server) Handler() http.Handler { return s.router }

// Store exposes the underlying store for advanced test setups (pre-seeding,
// assertions, etc.).
func (s *Server) Store() *Store { return s.store }

// Run blocks serving HTTP on Config.Listen.
func (s *Server) Run() error {
	PrintBannerToTTY(os.Stderr, s.startupCard())
	log.Printf("telegym-mock listening on %s", s.cfg.Listen)
	if s.cfg.DefaultWebhookURL != "" {
		log.Printf("default webhook URL: %s", s.cfg.DefaultWebhookURL)
	}
	s.startMetricsServer()
	return http.ListenAndServe(s.cfg.Listen, s.router)
}

// startupCard is the listen URL + bot env-var hint printed under the banner.
func (s *Server) startupCard() string {
	url := mockURL(s.cfg.Listen)
	var b strings.Builder
	b.WriteString("mock listening at  ")
	b.WriteString(url)
	b.WriteString("  (PID ")
	b.WriteString(strconv.Itoa(os.Getpid()))
	b.WriteString(")\n")
	b.WriteString("point your bot at  TELEGRAM_API_URL=")
	b.WriteString(url)
	b.WriteString("\n")
	if s.cfg.DefaultWebhookURL != "" {
		b.WriteString("default webhook URL ")
		b.WriteString(s.cfg.DefaultWebhookURL)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// mockURL turns a bind address into a clickable http://host:port URL.
func mockURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "http://localhost" + strings.TrimPrefix(addr, "0.0.0.0")
	}
	return "http://" + addr
}

// botMethod is the signature every Bot API endpoint conforms to.
type botMethod func(*gin.Context, *botEntry)

// withBot resolves the bot from the URL token segment and forwards to fn.
func (s *Server) withBot(fn botMethod) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.Param("token")
		if token == "" {
			c.JSON(http.StatusUnauthorized, APIResponse{OK: false, ErrorCode: 401, Description: "Unauthorized"})
			return
		}
		// Strip leading "bot" prefix in case routers vary (defensive).
		token = strings.TrimPrefix(token, "bot")
		bot := s.store.Bot(token)
		bot.touch()
		fn(c, bot)
	}
}

// registerRoutes wires all Bot API methods plus debug endpoints. Routes are
// registered for both `/bot<token>/<method>` and `/bot<token>/test/<method>`
// (telego uses the /test suffix when WithTestServerPath is set).
func (s *Server) registerRoutes() {
	// Index & health
	s.router.GET("/", s.index)
	s.router.GET("/health", s.health)

	// Debug API (test runner facing)
	s.router.POST("/debug/inject/update", s.handlers.DebugInjectUpdate)
	s.router.GET("/debug/messages/:token", s.handlers.DebugListMessages)
	s.router.POST("/debug/messages/:token/clear", s.handlers.DebugClearMessages)

	// File storage (multipart uploads from bot, fetched by telegym-proxy)
	s.router.GET("/debug/files", s.handlers.DebugListFiles)
	s.router.GET("/debug/files/:file_id", s.handlers.DebugServeFile)

	// Proxy registration (telegym-proxy binds chat_ids → forward webhook)
	s.router.POST("/debug/proxy/register", s.handlers.DebugProxyRegister)
	s.router.POST("/debug/proxy/unregister", s.handlers.DebugProxyUnregister)

	// Bot inventory: snapshot of every bot the mock has seen since startup.
	s.router.GET("/debug/bots", s.handlers.DebugListBots)

	// HTMX debug chat (human-facing real-user mode)
	defaultToken := s.cfg.DefaultToken
	if defaultToken == "" {
		defaultToken = "1234567890:telegym_default_mock_token_xxxxxxxx"
	}
	s.chat.register(s.router.Group("/debug/chat"), defaultToken)

	// Bot API endpoints - registered under both prefixes.
	for _, prefix := range []string{"/bot:token", "/bot:token/test"} {
		g := s.router.Group(prefix)
		s.registerBotAPI(g)
	}

	// Catch-all for any Bot API method without an explicit handler.
	// The dispatcher reads the embedded api.json spec and returns a
	// zero-value response of the declared return type, so even
	// rarely-used methods like getChat, forwardMessage, copyMessage,
	// createChatInviteLink etc. produce JSON that clients can unmarshal.
	s.router.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if !strings.HasPrefix(p, "/bot") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found", "path": p})
			return
		}
		token, methodName, ok := parseBotPath(p)
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "bad bot path", "path": p})
			return
		}
		bot := s.store.Bot(token)
		bot.touch()
		s.handlers.GenericDispatch(c, bot, methodName)
	})
}

// parseBotPath splits "/bot<token>/[test/]<method>" into its parts. Returns
// false for malformed paths.
func parseBotPath(p string) (token, method string, ok bool) {
	rest := strings.TrimPrefix(p, "/bot")
	idx := strings.IndexByte(rest, '/')
	if idx <= 0 {
		return "", "", false
	}
	token = rest[:idx]
	method = strings.TrimPrefix(rest[idx+1:], "test/")
	if method == "" {
		return "", "", false
	}
	return token, method, true
}

func (s *Server) registerBotAPI(g *gin.RouterGroup) {
	// Methods that return Message (or Update side-effect)
	g.POST("/sendMessage", s.withBot(s.handlers.SendMessage))
	g.POST("/sendPhoto", s.withBot(s.handlers.SendPhoto))
	g.POST("/sendVideo", s.withBot(s.handlers.SendVideo))
	g.POST("/sendAnimation", s.withBot(s.handlers.SendAnimation))
	g.POST("/sendSticker", s.withBot(s.handlers.SendSticker))
	g.POST("/sendDice", s.withBot(s.handlers.SendDice))
	g.POST("/editMessageText", s.withBot(s.handlers.EditMessageText))
	g.POST("/editMessageReplyMarkup", s.withBot(s.handlers.EditMessageReplyMarkup))
	g.POST("/editMessageMedia", s.withBot(s.handlers.EditMessageMedia))

	// Methods that return bool
	g.POST("/deleteMessage", s.withBot(s.handlers.DeleteMessage))
	g.POST("/answerCallbackQuery", s.withBot(s.handlers.AnswerCallbackQuery))

	// Identity / webhook lifecycle
	g.GET("/getMe", s.withBot(s.handlers.GetMe))
	g.POST("/getMe", s.withBot(s.handlers.GetMe))
	g.POST("/setWebhook", s.withBot(s.handlers.SetWebhook))
	g.POST("/deleteWebhook", s.withBot(s.handlers.DeleteWebhook))
	g.GET("/getWebhookInfo", s.withBot(s.handlers.GetWebhookInfo))
	g.POST("/getWebhookInfo", s.withBot(s.handlers.GetWebhookInfo))

	// Chat / member queries
	g.POST("/getChatMember", s.withBot(s.handlers.GetChatMember))
	g.GET("/getChatMember", s.withBot(s.handlers.GetChatMember))

}

func (s *Server) index(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"name":    "telegym mock",
		"version": "0.1.0",
	})
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
