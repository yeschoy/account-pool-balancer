package server

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestMistralUpstreamRequestIDPrecedenceBoundsAndIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, provider string
		headers        http.Header
		want           string
	}{
		{"native first", config.ProviderMistral, http.Header{"X-Request-Id": {"native"}, "Mistral-Correlation-Id": {"mistral"}, "X-Kong-Request-Id": {"kong"}}, "native"},
		{"correlation before kong", config.ProviderMistral, http.Header{"Mistral-Correlation-Id": {"mistral"}, "X-Kong-Request-Id": {"kong"}}, "mistral"},
		{"kong fallback", config.ProviderMistral, http.Header{"X-Kong-Request-Id": {"kong"}}, "kong"},
		{"invalid first skipped", config.ProviderMistral, http.Header{"X-Request-Id": {"bad\x00id"}, "Mistral-Correlation-Id": {"  mistral  "}}, "mistral"},
		{"oversized skipped", config.ProviderMistral, http.Header{"Mistral-Correlation-Id": {strings.Repeat("x", 513)}, "X-Kong-Request-Id": {"kong"}}, "kong"},
		{"empty stays unknown", config.ProviderMistral, http.Header{}, ""},
		{"other provider ignores mistral", config.ProviderRiver, http.Header{"Mistral-Correlation-Id": {"mistral"}, "X-Kong-Request-Id": {"kong"}}, ""},
		{"other provider unchanged", config.ProviderOpenCodeGo, http.Header{"X-Request-Id": {" legacy "}}, " legacy "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.headers.Clone()
			if got := upstreamRequestID(tc.headers, tc.provider); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			dst := make(http.Header)
			setUpstreamRequestID(dst, tc.headers, tc.provider)
			if dst.Get("X-Upstream-Request-Id") != tc.want || !reflect.DeepEqual(before, tc.headers) {
				t.Fatal("projection changed the provider's native headers or trace value")
			}
		})
	}
}
