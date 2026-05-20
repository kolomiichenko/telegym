package mock

import (
	"io"

	"github.com/kolomiichenko/telegym/pkg/branding"
)

// Banner re-exports the canonical telegym ASCII mascot for callers that
// already have pkg/mock in scope. The string itself lives in pkg/branding
// so the xk6 extension can share it without pulling in the mock server's
// dependency tree.
const Banner = branding.Banner

// PrintBanner writes the banner to w, mirroring branding.PrintBanner.
func PrintBanner(w io.Writer) { branding.PrintBanner(w) }

// PrintBannerToTTY mirrors branding.PrintBannerToTTY - prints the
// colored banner plus info to /dev/tty so they survive backgrounded
// launches with stdout/stderr redirected to a log file.
func PrintBannerToTTY(fallback io.Writer, info string) {
	branding.PrintBannerToTTY(fallback, info)
}
