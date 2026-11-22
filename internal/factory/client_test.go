package factory

import (
	"net/http"
	"testing"
)

func TestIsAmpClient(t *testing.T) {
	amp := http.Header{}
	amp.Set("X-Amp-Thread-Id", "T-1")
	raw := http.Header{"x-amp-client": {"cli"}} // non-canonical key from a raw map
	for _, tc := range []struct {
		h    http.Header
		want bool
	}{
		{amp, true},
		{raw, true},
		{http.Header{"User-Agent": {"amp/1.0"}}, false},
		{http.Header{"X-Amp": {"1"}}, false},
		{http.Header{"X-Ample-Id": {"1"}}, false},
		{nil, false},
	} {
		if got := IsAmpClient(tc.h); got != tc.want {
			t.Fatalf("IsAmpClient(%v) = %v, want %v", tc.h, got, tc.want)
		}
	}
}
