package branding

import (
	"bytes"
	"strings"
	"testing"
)

// TestStripANSIRemovesColorSequences verifies the hand-rolled scanner in
// stripANSI handles single colors, multi-component sequences (`\033[1;36m`),
// and 256-color codes (`\033[38;5;214m`) -- the three forms the banner uses.
// Plain text must survive untouched.
func TestStripANSIRemovesColorSequences(t *testing.T) {
	cases := map[string]string{
		"plain text":                       "plain text",
		"\033[31mred\033[0m":               "red",
		"\033[1;36mbold cyan\033[0m":       "bold cyan",
		"\033[1;38;5;214mamber\033[0m":     "amber",
		"a\033[33mb\033[37mc\033[0md":      "abcd",
		"reset only \033[0m here":          "reset only  here",
		"\033[1;36m\033[37mlayered\033[0m": "layered",
	}
	for in, want := range cases {
		if got := stripANSI(in); got != want {
			t.Errorf("stripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStripANSIPreservesESCNotFollowedByBracket guards against the scanner
// eating bare ESC bytes that are not part of a CSI sequence -- only `ESC [
// ... m` should be consumed.
func TestStripANSIPreservesESCNotFollowedByBracket(t *testing.T) {
	in := "before\x1bafter"
	got := stripANSI(in)
	if got != in {
		t.Errorf("stripANSI(%q) = %q, want unchanged", in, got)
	}
}

// TestBannerContainsExpectedTokens is a sanity check that the banner
// constant still spells out the brand once ANSI codes are removed. Cheap
// guard against accidental edits that mangle the ASCII art payload.
func TestBannerContainsExpectedTokens(t *testing.T) {
	plain := stripANSI(Banner)
	for _, want := range []string{
		"████████", // T cross-bar from "TELE"
		"load testing for telegram bots",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("Banner missing %q", want)
		}
	}
}

// TestPrintBannerStripsForNonTTY confirms PrintBanner removes ANSI when
// writing to a non-terminal (here, a bytes.Buffer). Keeps log files
// readable without ESC noise.
func TestPrintBannerStripsForNonTTY(t *testing.T) {
	var buf bytes.Buffer
	PrintBanner(&buf)
	if buf.Len() == 0 {
		t.Fatal("PrintBanner wrote nothing")
	}
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("PrintBanner left ANSI escapes in non-TTY output:\n%s", buf.String())
	}
}
