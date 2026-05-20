package telegym

import (
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"go.k6.io/k6/v2/js/modules"
)

// Client holds shared state across VUs: HTTP transport, mock URL, bot token.
// Methods on Client are concurrency-safe.
type Client struct {
	mockURL string
	token   string
	http    *http.Client
	userSeq atomic.Int64
}

// NewClient configures itself from env vars:
//
//	TELEGYM_MOCK_URL    default http://localhost:5678
//	TELEGYM_BOT_TOKEN   default 1234567890:telegym_default_mock_token_xxxxxxxx
func NewClient() *Client {
	return &Client{
		mockURL: envOr("TELEGYM_MOCK_URL", "http://localhost:5678"),
		token:   envOr("TELEGYM_BOT_TOKEN", "1234567890:telegym_default_mock_token_xxxxxxxx"),
		http: &http.Client{
			// Per-request timeout is short; long awaits poll multiple times.
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        2000,
				MaxIdleConnsPerHost: 2000,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,
			},
		},
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// NewUser allocates a User with a stable, unique chat/user ID. Pass an explicit
// chatID to reuse identity across iterations (e.g. for a registered user).
func (c *Client) NewUser(chatID int64, vu modules.VU) *User {
	if chatID == 0 {
		chatID = c.allocateID(vu)
	}
	return &User{
		client: c,
		vu:     vu,
		ChatID: chatID,
		UserID: chatID,
		// Sub-second resolution would be ideal but telegym-mock dates are unix
		// seconds. Subtract 1 so messages from this exact second are visible.
		since: time.Now().Unix() - 1,
	}
}

// allocateID derives an ID space per VU to avoid collisions across iterations
// of the same scenario: vuID in the high bits, a monotonic seq in the low
// bits, so IDs are unique within a run yet readable in logs.
func (c *Client) allocateID(vu modules.VU) int64 {
	seq := c.userSeq.Add(1)
	var vuID int64 = 1
	if vu != nil && vu.State() != nil {
		vuID = int64(vu.State().VUID)
	}
	return vuID*1_000_000 + seq
}
