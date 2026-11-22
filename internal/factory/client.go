package factory

import (
	"net/http"
	"strings"
)

// IsAmpClient reports whether a request came from the Amp CLI. Amp marks every
// model call with its own X-Amp-* headers (X-Amp-Thread-Id on the Gemini
// route, observed 2026-11-22). This is a client-supplied performance hint, not
// authentication. The check walks only the header names, never the body.
func IsAmpClient(h http.Header) bool {
	for name := range h {
		if len(name) > 6 && strings.EqualFold(name[:6], "x-amp-") {
			return true
		}
	}
	return false
}
