// Package branding holds the telegym banner. Stdlib-only so the xk6
// module can import it without pulling in the mock's HTTP stack.
package branding

import (
	"io"
	"os"
	"strings"
)

// Banner is the telegym wordmark + dumbbell ASCII art with ANSI colors.
const Banner = "\n" +
	"\033[1;36m" +
	"████████╗███████╗██╗     ███████╗\n" +
	"╚══██╔══╝██╔════╝██║     ██╔════╝         \033[37m██╗           ██╗\033[1;36m\n" +
	"   ██║   █████╗  ██║     █████╗        \033[37m██╗██║           ██║██╗\033[1;36m\n" +
	"   ██║   ██╔══╝  ██║     ██╔══╝     \033[37m██╗██║██║           ██║██║██╗\033[1;36m\n" +
	"   ██║   ███████╗███████╗███████╗   \033[37m██║██║██║           ██║██║██║\033[1;36m\n" +
	"   ╚═╝   ╚══════╝╚══════╝╚══════╝   \033[37m██║██║██║██████████╗██║██║██║\033[1;36m\n" +
	"\033[1;38;5;214m" +
	"   ██████╗ ██╗   ██╗███╗   ███╗     \033[37m██║██║██║██████████║██║██║██║\033[1;38;5;214m\n" +
	"  ██╔════╝  ██╗ ██╔╝████╗ ████║     \033[37m██║██║██║╚═════════╝██║██║██║\033[1;38;5;214m\n" +
	"  ██║  ███╗  ████╔╝ ██╔████╔██║     \033[37m██║██║██║           ██║██║██║\033[1;38;5;214m\n" +
	"  ██║   ██║   ██╔╝  ██║ ██╔╝██║     \033[37m╚═╝██║██║           ██║██║╚═╝\033[1;38;5;214m\n" +
	"   ██████╔╝   ██║   ██║ ╚═╝ ██║        \033[37m╚═╝██║           ██║╚═╝\033[1;38;5;214m\n" +
	"   ╚═════╝    ╚═╝   ╚═╝     ╚═╝           \033[37m╚═╝           ╚═╝\033[1;38;5;214m\n" +
	"\033[0m" +
	"                 load testing for telegram bots\n" +
	"\n"

// PrintBanner writes Banner to w, stripping ANSI when w is not a TTY.
func PrintBanner(w io.Writer) {
	out := Banner
	if !isTerminal(w) {
		out = stripANSI(Banner)
	}
	_, _ = io.WriteString(w, out)
}

// PrintBannerToTTY writes the colored banner and info to /dev/tty so
// they survive stdout/stderr redirection. Falls back to fallback (with
// ANSI stripped) when /dev/tty cannot be opened.
func PrintBannerToTTY(fallback io.Writer, info string) {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		PrintBanner(fallback)
		_, _ = io.WriteString(fallback, info)
		return
	}
	defer func() { _ = tty.Close() }()
	_, _ = io.WriteString(tty, Banner)
	_, _ = io.WriteString(tty, info)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// stripANSI removes CSI color sequences (\x1b[...m).
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == ';') {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
