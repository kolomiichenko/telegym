package telegym

import "testing"

// TestNewUserAutoAllocatesUniqueIDs is the basic xk6 contract: passing
// chatID=0 must give the caller a unique-per-call User. Two consecutive
// calls returning the same chat_id would silently merge virtual users
// in a load test and ruin every measurement.
//
// We pass `nil` for the VU - allocateID handles it (defaults to vuID=1)
// and the test doesn't need a real k6 runtime to verify ID uniqueness.
func TestNewUserAutoAllocatesUniqueIDs(t *testing.T) {
	c := NewClient()

	const n = 50
	seen := make(map[int64]bool, n)
	for i := range n {
		u := c.NewUser(0, nil)
		if u == nil {
			t.Fatalf("NewUser(0) returned nil at iter %d", i)
		}
		if u.ChatID == 0 {
			t.Fatalf("NewUser(0) returned zero ChatID at iter %d", i)
		}
		if seen[u.ChatID] {
			t.Errorf("NewUser(0) collision: ChatID %d already issued", u.ChatID)
		}
		seen[u.ChatID] = true
		// UserID must mirror ChatID for auto-allocated users; scenarios
		// rely on this to inject callback queries from the same identity.
		if u.UserID != u.ChatID {
			t.Errorf("UserID (%d) != ChatID (%d) for auto-allocated user", u.UserID, u.ChatID)
		}
	}
}

// TestNewUserPreservesExplicitID covers the "registered user replay"
// case: scenarios that loaded a chat_id from a pool file must get a
// User pinned to that exact ID, not a freshly-allocated one.
func TestNewUserPreservesExplicitID(t *testing.T) {
	c := NewClient()

	const explicit int64 = 1_234_567
	u := c.NewUser(explicit, nil)
	if u == nil {
		t.Fatal("NewUser(explicit) returned nil")
	}
	if u.ChatID != explicit {
		t.Errorf("ChatID = %d, want %d", u.ChatID, explicit)
	}
	if u.UserID != explicit {
		t.Errorf("UserID = %d, want %d", u.UserID, explicit)
	}

	// Two NewUser calls with the same explicit ID must return distinct
	// User structs (separate state buckets) but the same identity.
	u2 := c.NewUser(explicit, nil)
	if u2 == u {
		t.Error("NewUser(explicit) returned the same *User pointer on repeated calls")
	}
	if u2.ChatID != explicit {
		t.Errorf("second call ChatID = %d, want %d", u2.ChatID, explicit)
	}
}

// TestNewClientDefaults guards against accidental config drift in the
// env-fallback logic. Scenarios run without any env setup and must hit
// the default mock URL and the documented default token.
func TestNewClientDefaults(t *testing.T) {
	// Unset both env vars to exercise the fallback path even if the
	// developer running the test has them set in their shell.
	t.Setenv("TELEGYM_MOCK_URL", "")
	t.Setenv("TELEGYM_BOT_TOKEN", "")

	c := NewClient()
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if c.mockURL != "http://localhost:5678" {
		t.Errorf("default mockURL = %q, want http://localhost:5678", c.mockURL)
	}
	if c.token == "" {
		t.Error("default token is empty")
	}
	if c.http == nil {
		t.Error("Client.http is nil; scenarios would NPE on first call")
	}
}
