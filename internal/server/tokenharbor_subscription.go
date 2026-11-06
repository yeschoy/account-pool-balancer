package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

// Every import rechecks with the saved session and fixed egress. A prior
// observation, a model catalog or a saved checkout never grants admission.
func (s *Server) thCheckSubscription(ctx context.Context, id string) (thsignupstore.Row, error) {
	store, err := s.thStore()
	if err != nil {
		return thsignupstore.Row{}, err
	}
	row, err := store.Get(id)
	if err != nil {
		return row, err
	}
	if row.Status == thsignupstore.Running || row.Status == thsignupstore.Queued || row.Status == thsignupstore.Saved {
		return row, errors.New("请先完成注册，再检测套餐")
	}
	cfg := s.cfg.Get().TokenHarbor
	proxy := row.Egress(cfg.ProxyURL)
	flow, err := thregister.New(thregister.Config{BaseURL: thBaseURL(cfg.BaseURL), ProxyURL: proxy, UserAgent: thRowUserAgent(row), Seed: row.ID, EgressRegion: thEgressRegion(cfg, proxy), RequestTimeout: 40 * time.Second})
	if err != nil {
		return row, errors.New("套餐检测代理无效")
	}
	defer flow.Close()
	flow.SeedCookies(thStoredResult(row).Cookies)
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	plan, checkErr := flow.CheckSubscription(ctx)
	updated, err := store.Update(id, func(rw *thsignupstore.Row) error {
		current := s.cfg.Get().TokenHarbor
		if rw.UpdatedAt != row.UpdatedAt || current.BaseURL != cfg.BaseURL || current.ProxyURL != cfg.ProxyURL {
			return errors.New("套餐检测期间账号或上游设置变化，请重新检测")
		}
		rw.Subscription = plan
		res := thStoredResult(*rw)
		res.Cookies = flow.Session()
		thSaveResult(rw, res)
		return nil
	})
	if err != nil {
		return row, err
	}
	return updated, checkErr
}

func (s *Server) apiTHSubscription(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	row, err := s.thCheckSubscription(r.Context(), r.PathValue("id"))
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, thsignupstore.SummaryOf(row))
}
