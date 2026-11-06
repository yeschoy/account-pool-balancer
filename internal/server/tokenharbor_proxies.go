package server

import (
	"crypto/rand"
	"errors"
	"math/big"
	"net/http"

	"account-pool-balancer/internal/thsignupstore"
)

// NoEgressWarning is the log line queued when a registration has no proxy at
// all and will therefore run from this host's own address. Such a run exposes
// the host to the site and to every third party the flow touches (mailbox,
// captcha), and the exposure is what an operator needs to notice; it is
// reported rather than refused because a deployment may deliberately register
// from a region the API serves directly.
const NoEgressWarning = "未绑定代理且无默认代理：本次注册将从本机 IP 出网"

func thAssignProxies(rows []thsignupstore.Row, proxies []string, fallback, mode string) error {
	for i := range rows {
		if rows[i].ProxyURL != "" {
			continue
		}
		if len(proxies) == 0 {
			rows[i].ProxyURL = fallback
			continue
		}
		n := i % len(proxies)
		if mode == "random" {
			v, err := rand.Int(rand.Reader, big.NewInt(int64(len(proxies))))
			if err != nil {
				return err
			}
			n = int(v.Int64())
		}
		rows[i].ProxyURL = proxies[n]
	}
	return nil
}

func (s *Server) apiTHProxies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, thProxyView(store.Proxies()))
}

func (s *Server) apiTHSaveProxies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Proxies *[]string `json:"proxies"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	if in.Proxies == nil {
		apiError(w, http.StatusBadRequest, errors.New("请提供 proxies 数组；清空时明确传入空数组"))
		return
	}
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	if err := store.SaveProxies(*in.Proxies); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, thProxyView(store.Proxies()))
}

func thProxyView(proxies []string) map[string]any {
	labels := make([]string, len(proxies))
	for i, proxy := range proxies {
		labels[i] = thsignupstore.ProxyLabel(proxy)
	}
	return map[string]any{"count": len(proxies), "labels": labels}
}
