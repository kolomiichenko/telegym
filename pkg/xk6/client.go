package telegym

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
	// Sub-second resolution would be ideal but telegym-mock dates are unix
	// seconds. Subtract 1 so messages from this exact second are visible.
	since := time.Now().Unix() - 1
	return &User{
		client:         c,
		vu:             vu,
		ChatID:         chatID,
		UserID:         chatID,
		since:          since,
		requestsFrom:   since,
		seenRequestSeq: make(map[string]int64),
		callbackIDs:    make(map[string]struct{}),
	}
}

// requests reads the newest journal entries, oldest first. An empty method
// returns every method; limit 0 uses the mock's default.
func (c *Client) requests(method string, limit int) ([]APIRequest, error) {
	query := url.Values{}
	if method != "" {
		query.Set("method", method)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	resp, err := c.http.Get(c.mockURL + "/debug/requests/" + c.token + "?" + query.Encode())
	if err != nil {
		return nil, err
	}
	var out struct {
		Requests []APIRequest `json:"requests"`
	}
	if err := decodeDebug(resp, &out); err != nil {
		return nil, err
	}
	return out.Requests, nil
}

// clearRequests empties the bot's journal and returns how many calls it held.
func (c *Client) clearRequests() (int, error) {
	resp, err := c.http.Post(c.mockURL+"/debug/requests/"+c.token+"/clear", "application/json", nil)
	if err != nil {
		return 0, err
	}
	var out struct {
		Cleared int `json:"cleared"`
	}
	if err := decodeDebug(resp, &out); err != nil {
		return 0, err
	}
	return out.Cleared, nil
}

// decodeDebug closes resp after decoding its JSON body into out, turning a
// non-2xx status into an error carrying the response body.
func decodeDebug(resp *http.Response, out any) error {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %d %s", resp.Request.URL.Path, resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
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
