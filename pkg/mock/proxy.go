package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Proxy registration lets an external relay (cmd/telegym-proxy) bind a set of
// chat_ids to a webhook URL. When the bot-under-test sends an outbound
// message for one of those chats, the mock POSTs the Message to the
// registered webhook so the relay can forward it to a real Telegram
// conversation - enabling tests from a real Telegram client UI.

type proxyForward struct {
	WebhookURL string
}

// proxyRegistry maps (token, chat_id) → forward target. Lives on Store
// so the dispatch loop in AppendMessage can look it up without circular
// dependencies on the chat package.
type proxyRegistry struct {
	mu      sync.RWMutex
	entries map[string]map[int64]proxyForward // token → chat_id → forward
	client  *http.Client
	metrics *Metrics // optional - counts forwards by result
}

func newProxyRegistry() *proxyRegistry {
	return &proxyRegistry{
		entries: map[string]map[int64]proxyForward{},
		client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 16,
				IdleConnTimeout:     60 * time.Second,
			},
		},
	}
}

// Register binds chat_ids → webhook URL for a bot token. Replaces any prior
// binding for the same (token, chat_id) tuples.
func (p *proxyRegistry) Register(token, webhookURL string, chatIDs []int64) {
	p.mu.Lock()
	if p.entries[token] == nil {
		p.entries[token] = map[int64]proxyForward{}
	}
	for _, id := range chatIDs {
		p.entries[token][id] = proxyForward{WebhookURL: webhookURL}
	}
	p.mu.Unlock()
	p.updateBindingsGauge()
}

// Unregister removes specific chat_id bindings, or all bindings for the
// token if chatIDs is empty.
func (p *proxyRegistry) Unregister(token string, chatIDs []int64) {
	p.mu.Lock()
	if len(chatIDs) == 0 {
		delete(p.entries, token)
	} else if m := p.entries[token]; m != nil {
		for _, id := range chatIDs {
			delete(m, id)
		}
	}
	p.mu.Unlock()
	p.updateBindingsGauge()
}

// Lookup returns the forward URL for (token, chat_id) or "" if not registered.
func (p *proxyRegistry) Lookup(token string, chatID int64) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if m := p.entries[token]; m != nil {
		return m[chatID].WebhookURL
	}
	return ""
}

func (p *proxyRegistry) countForward(result string) {
	if p.metrics == nil {
		return
	}
	p.metrics.ProxyForwardsTotal.WithLabelValues(result).Inc()
}

func (p *proxyRegistry) updateBindingsGauge() {
	if p.metrics == nil {
		return
	}
	p.mu.RLock()
	n := 0
	for _, m := range p.entries {
		n += len(m)
	}
	p.mu.RUnlock()
	p.metrics.ProxyBindings.Set(float64(n))
}

// Dispatch POSTs a Message to the registered webhook for a (token, chat_id),
// fire-and-forget. Failures are logged but never propagate to the caller --
// proxy outage should not break the bot loop or the message store.
func (p *proxyRegistry) Dispatch(token string, m Message) {
	url := p.Lookup(token, m.Chat.ID)
	if url == "" {
		return
	}
	body, err := json.Marshal(proxyForwardPayload{
		Token:   token,
		Message: m,
	})
	if err != nil {
		log.Printf("proxy: marshal: %v", err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.client.Do(req)
		if err != nil {
			p.countForward("error")
			log.Printf("proxy forward → %s: %v", url, err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode >= 400 {
			p.countForward("http_error")
			log.Printf("proxy forward → %s: HTTP %d", url, resp.StatusCode)
			return
		}
		p.countForward("ok")
	}()
}

// proxyForwardPayload is what the mock POSTs to the proxy webhook. The proxy
// uses Message.Chat.ID as the real Telegram chat to relay into, fetches any
// referenced file_ids from /debug/files, and calls the real Bot API.
type proxyForwardPayload struct {
	Token   string  `json:"token"`
	Message Message `json:"message"`
}

// =====================================================================
// HTTP endpoints
// =====================================================================

type proxyRegisterReq struct {
	Token      string  `json:"token"      binding:"required"`
	WebhookURL string  `json:"webhook_url" binding:"required"`
	ChatIDs    []int64 `json:"chat_ids"   binding:"required"`
}

// DebugProxyRegister wires up the mock to forward outbound messages for
// the given (token, chat_ids) to webhookURL.
func (h *Handlers) DebugProxyRegister(c *gin.Context) {
	var req proxyRegisterReq
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.ChatIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat_ids must be non-empty"})
		return
	}
	h.Store.Proxy.Register(req.Token, req.WebhookURL, req.ChatIDs)
	c.JSON(http.StatusOK, gin.H{"ok": true, "registered": len(req.ChatIDs)})
}

type proxyUnregisterReq struct {
	Token   string  `json:"token" binding:"required"`
	ChatIDs []int64 `json:"chat_ids"` // empty = unregister all for token
}

func (h *Handlers) DebugProxyUnregister(c *gin.Context) {
	var req proxyUnregisterReq
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.Store.Proxy.Unregister(req.Token, req.ChatIDs)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DebugServeFile returns previously-captured multipart bytes by file_id, so
// the proxy can re-upload them to real Telegram.
func (h *Handlers) DebugServeFile(c *gin.Context) {
	id := c.Param("file_id")
	f, ok := h.Store.Files.Get(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found", "file_id": id})
		return
	}
	ct := f.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	c.Header("Content-Type", ct)
	c.Header("Content-Length", strconv.Itoa(len(f.Bytes)))
	if f.Filename != "" {
		c.Header("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, f.Filename))
	}
	c.Header("X-Mock-Kind", f.Kind)
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(f.Bytes)
}

// DebugListFiles is a debugging convenience: shows store stats and the
// most recent file_ids without contents.
func (h *Handlers) DebugListFiles(c *gin.Context) {
	count, bytes := h.Store.Files.Stats()
	c.JSON(http.StatusOK, gin.H{
		"count":       count,
		"total_bytes": bytes,
		"max_bytes":   h.Store.Files.maxBytes,
	})
}
