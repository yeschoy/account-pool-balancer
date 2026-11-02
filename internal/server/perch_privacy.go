package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"account-pool-balancer/internal/perch"
)

// Turning off Perch's data collection on a freshly registered account.
//
// A new Perch account starts with its data-program switch in whatever state
// the product defaults to, and the Settings page is the only place the web app
// lets its owner change it. Registering accounts in bulk through this console
// never opens that page, so every account it creates would keep collecting
// unless the switch is written from here. Both registration paths (the
// console's own job and the automatic pool refill) do it, after activation and
// before the account is imported: the account is then usable from the pool
// with collection already off, and no imported account has to be revisited.

// perchSessionAccess extracts the access token and app host from one exported
// credential document. Both registration paths hold the document before it is
// imported, so this is the one place that knows its envelope shape.
func perchSessionAccess(doc []byte) (accessToken, appURL string, err error) {
	var envelope struct {
		Session json.RawMessage `json:"supabase_session"`
		Auth    struct {
			URL string `json:"supabase_url"`
		} `json:"auth_context"`
	}
	if len(doc) == 0 || json.Unmarshal(doc, &envelope) != nil || len(envelope.Session) == 0 {
		return "", "", errors.New("会话无效")
	}
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(envelope.Session, &session) != nil || strings.TrimSpace(session.AccessToken) == "" {
		return "", "", errors.New("会话无效")
	}
	return session.AccessToken, strings.TrimSpace(envelope.Auth.URL), nil
}

// disablePerchDataProgram sends the data-program switch to off for one
// session, through the proxy that registered it — the same exit the account
// was created from, so the switch is not written from a second address.
//
// appURL is the auth_context host when it names one, and Perch's app host
// otherwise. The data-program route is Perch's own, not Supabase's, so a
// Supabase URL is never used for it.
func (s *Server) disablePerchDataProgram(ctx context.Context, proxyURL string, doc []byte) (resultErr error) {
	started := time.Now()
	token, appURL, err := perchSessionAccess(doc)
	if err != nil {
		return err
	}
	if !strings.Contains(appURL, "perchai.app") {
		appURL = ""
	}
	// The switch is worth a short wait and no more: it runs inside the
	// registration's own deadline, and a slow answer must not cost the
	// account that is already activated.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	defer func() {
		s.logPerchDiagnostic("", "privacy", autoPerchProxySource(proxyURL), perch.AppURL, started, resultErr)
	}()
	_, resultErr = s.perchClient().SetDataProgram(perch.WithProxy(ctx, proxyURL), appURL, token, perch.PrivacyOff())
	return resultErr
}
