package factory

import (
	"crypto/rand"
	"encoding/hex"
)

// Traceparent renders a W3C trace context in the shape droid sends:
// 00-<32 hex trace id>-<16 hex span id>-01. Empty or non-hex inputs yield "",
// so a caller never forwards a malformed header.
func Traceparent(traceID, spanID string) string {
	if !hexN(traceID, 32) || !hexN(spanID, 16) {
		return ""
	}
	if traceID == "00000000000000000000000000000000" || spanID == "0000000000000000" {
		return ""
	}
	return "00-" + traceID + "-" + spanID + "-01"
}

// RandomTraceparent is for reads that have no conversation to derive ids from
// (quota polls). droid labels such requests the same way.
func RandomTraceparent() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return "00-" + hex.EncodeToString(b[:16]) + "-" + hex.EncodeToString(b[16:]) + "-01"
}

func hexN(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}
