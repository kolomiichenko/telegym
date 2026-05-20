package mock

import "testing"

// TestStoreBotIdempotent verifies Bot(token) returns the same entry on
// repeated calls -- the auto-registration must not overwrite state. This
// is the contract every handler relies on: token in, same *botEntry out.
func TestStoreBotIdempotent(t *testing.T) {
	s := NewStore()
	first := s.Bot("123:abc")
	second := s.Bot("123:abc")
	if first != second {
		t.Errorf("Bot(\"123:abc\") returned different pointers on repeated calls: %p vs %p", first, second)
	}

	other := s.Bot("999:xyz")
	if other == first {
		t.Errorf("Bot(\"999:xyz\") returned the same pointer as Bot(\"123:abc\")")
	}
}

// TestStoreBotsSnapshot exercises the inventory snapshot used by
// /debug/bots and the chat home page. After touching three bots, the
// snapshot must list all three with their (token, identity) intact.
func TestStoreBotsSnapshot(t *testing.T) {
	s := NewStore()
	tokens := []string{"100:aaa", "200:bbb", "300:ccc"}
	for _, tok := range tokens {
		s.Bot(tok) // auto-register
	}

	got := s.Bots()
	if len(got) != len(tokens) {
		t.Fatalf("Bots() returned %d entries, want %d", len(got), len(tokens))
	}

	// Every registered token must appear in TokenFull -- we don't assume
	// order, since Bots() sorts by LastSeen which is set with second-level
	// granularity and may tie.
	seen := make(map[string]bool, len(tokens))
	for _, info := range got {
		seen[info.TokenFull] = true
		if info.TokenShort == "" {
			t.Errorf("BotInfo for %q has empty TokenShort", info.TokenFull)
		}
		if info.FirstSeen == 0 {
			t.Errorf("BotInfo for %q has zero FirstSeen", info.TokenFull)
		}
	}
	for _, tok := range tokens {
		if !seen[tok] {
			t.Errorf("Bots() snapshot missing token %q", tok)
		}
	}
}

// TestStoreCountersMonotonic guarantees NextMessageID and NextUpdateID
// hand out strictly increasing values. Scenarios depend on this for
// dedup; a regression here corrupts every test's view of bot replies.
func TestStoreCountersMonotonic(t *testing.T) {
	s := NewStore()

	prevMsg := s.NextMessageID()
	for i := range 100 {
		next := s.NextMessageID()
		if next <= prevMsg {
			t.Fatalf("NextMessageID went backwards: %d <= %d (iteration %d)", next, prevMsg, i)
		}
		prevMsg = next
	}

	prevUpd := s.NextUpdateID()
	for i := range 100 {
		next := s.NextUpdateID()
		if next <= prevUpd {
			t.Fatalf("NextUpdateID went backwards: %d <= %d (iteration %d)", next, prevUpd, i)
		}
		prevUpd = next
	}
}
