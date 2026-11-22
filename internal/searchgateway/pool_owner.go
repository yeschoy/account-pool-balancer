package searchgateway

import (
	"context"
	"io"
	"net/http"
	"strings"
)

const ownerHeader = "X-APB-Owner-Token"

type ownerContextKey struct{}
type ownerTokenState struct{ token string }

// Preflight before any model invocation. Old pools cannot silently ignore the
// safety requirement and charge an unpinned generation.
func (g *Gateway) checkPoolOwner(r *http.Request, route Route) error {
	probe := r.Clone(r.Context())
	probe.Method = http.MethodGet
	resp, err := g.request(probe, route, "/v1/pool-capabilities", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &gatewayError{503, "pool_owner_unsupported", "Upstream pool does not advertise request-owner v1; no model call was sent"}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(b) > 4096 {
		return upstream("invalid_pool_capabilities")
	}
	o, err := decodeObject(b)
	ttl, valid := intField(o, "ttlSec")
	if err != nil || str(o["requestOwner"]) != "v1" || str(o["protocol"]) != "responses" || !valid || ttl < int64(g.cfg.RequestTimeoutSec) {
		return &gatewayError{503, "pool_owner_unsupported", "Pool owner capability or TTL does not meet the configured deadline"}
	}
	return nil
}
func withOwner(r *http.Request, state *ownerTokenState) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ownerContextKey{}, state))
}
func acceptPoolOwner(resp *http.Response, state *ownerTokenState) error {
	values := resp.Header.Values(ownerHeader)
	if len(values) != 1 {
		return upstream("pool_owner_missing")
	}
	token := values[0]
	if len(token) > 2048 || len(token) < 40 || !strings.HasPrefix(token, "ow1.") {
		return upstream("pool_owner_invalid")
	}
	for _, c := range token[4:] {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return upstream("pool_owner_invalid")
		}
	}
	if state.token != "start" && token != state.token {
		return upstream("pool_owner_changed")
	}
	state.token = token
	return nil
}
