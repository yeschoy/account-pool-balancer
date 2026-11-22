package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

const requestOwnerHeader = "X-APB-Owner-Token" // -Token is always secret in logs
const requestOwnerTTL = 5 * time.Minute
const requestOwnerPrefix = "ow1."

var errRequestOwner = errors.New("request owner invalid, expired or unavailable; the request was not sent")

type requestOwnerClaims struct {
	Version    int    `json:"v"`
	Account    string `json:"a"`
	Source     string `json:"s"`
	Client     string `json:"c"`
	ClientHash string `json:"h"`
	Group      string `json:"g"`
	Model      string `json:"m"`
	Expires    int64  `json:"e"`
}
type requestOwner struct {
	enabled bool
	claims  *requestOwnerClaims
	token   string
}

func (s *Server) ownerCipher() (cipher.AEAD, error) {
	key := hmac.New(sha256.New, s.secret)
	key.Write([]byte("account-pool-balancer/request-owner/v1"))
	block, err := aes.NewCipher(key.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (s *Server) readRequestOwner(cfg *config.Config, ck config.ClientKey, model, path string, h http.Header, now time.Time) (requestOwner, error) {
	values := h.Values(requestOwnerHeader)
	if len(values) == 0 {
		return requestOwner{}, nil
	}
	out := requestOwner{enabled: true}
	if len(values) != 1 || keyPool(cfg, ck) != config.ProviderMistral || path != "/v1/responses" {
		return out, errRequestOwner
	}
	token := values[0]
	if token == "start" {
		return out, nil
	}
	if len(token) > 2048 || !strings.HasPrefix(token, requestOwnerPrefix) {
		return out, errRequestOwner
	}
	aead, err := s.ownerCipher()
	if err != nil {
		return out, errRequestOwner
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, requestOwnerPrefix))
	if err != nil || len(b) < aead.NonceSize()+aead.Overhead() {
		return out, errRequestOwner
	}
	plain, err := aead.Open(nil, b[:aead.NonceSize()], b[aead.NonceSize():], []byte(requestOwnerPrefix))
	if err != nil {
		return out, errRequestOwner
	}
	var claims requestOwnerClaims
	if json.Unmarshal(plain, &claims) != nil || claims.Version != 1 || claims.Client != ck.ID || claims.ClientHash != ck.KeyHash || claims.Group != ck.Group || claims.Model != model || claims.Expires <= now.Unix() || claims.Expires > now.Add(requestOwnerTTL).Unix() {
		return out, errRequestOwner
	}
	a, ok := cfg.Account(claims.Account)
	if !ok || !a.Enabled || a.Provider != config.ProviderMistral || a.Group != ck.Group || claims.Source != quota.ResponseBindingSource(cfg, a) {
		return out, errRequestOwner
	}
	out.claims = &claims
	out.token = token
	return out, nil
}
func (s *Server) ownerForLease(cfg, selectedCfg *config.Config, ck config.ClientKey, model string, a config.Account, owner requestOwner, now time.Time) (string, error) {
	currentKey, keyOK := cfg.ClientKey(ck.ID)
	current, ok := cfg.Account(a.ID)
	if !keyOK || !currentKey.Enabled || currentKey.KeyHash != ck.KeyHash || currentKey.Group != ck.Group || !ok || !current.Enabled || current.Group != ck.Group || current.Provider != config.ProviderMistral || quota.ResponseBindingSource(cfg, current) != quota.ResponseBindingSource(selectedCfg, a) {
		return "", errRequestOwner
	}
	if owner.claims != nil {
		if owner.claims.Account != a.ID || owner.claims.Expires <= now.Unix() || owner.claims.Source != quota.ResponseBindingSource(cfg, a) {
			return "", errRequestOwner
		}
		return owner.token, nil // Do not renew deadline or change owners mid-request.
	}
	claims := requestOwnerClaims{Version: 1, Account: a.ID, Source: quota.ResponseBindingSource(cfg, a), Client: ck.ID, ClientHash: ck.KeyHash, Group: ck.Group, Model: model, Expires: now.Add(requestOwnerTTL).Unix()}
	plain, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	aead, err := s.ownerCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, plain, []byte(requestOwnerPrefix))
	return requestOwnerPrefix + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Capabilities is authenticated but read-only: no model, quota or RPM call.
func (s *Server) handlePoolCapabilities(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	ck, ok := s.authClient(cfg, r)
	w.Header().Set("Cache-Control", "no-store")
	if !ok {
		writeLocalError(w, "openai", 401, "authentication_error", "invalid_api_key", "invalid API key", 0)
		return
	}
	provider := keyPool(cfg, ck)
	if !cfg.PoolEnabled(provider) {
		writeLocalError(w, "openai", 503, "api_error", "pool_disabled", "this pool is disabled", 0)
		return
	}
	if provider != config.ProviderMistral {
		writeLocalError(w, "openai", 404, "invalid_request_error", "unsupported_capability", "request-owner tokens are not implemented for this pool", 0)
		return
	}
	writeJSON(w, 200, map[string]any{"requestOwner": "v1", "ttlSec": int(requestOwnerTTL / time.Second), "protocol": "responses"})
}
