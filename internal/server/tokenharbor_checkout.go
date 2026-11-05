package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

// Links are deliberately absent from registration summaries and live logs.
func (s *Server) apiTHSavedCheckout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	row, err := store.Get(r.PathValue("id"))
	if err != nil {
		s.thStoreError(w, err)
		return
	}
	if row.Checkout == nil || !thregister.ValidCheckoutURL(row.Checkout.URL) {
		apiError(w, http.StatusNotFound, errors.New("尚无已保存支付链接；如上次结果不确定，请先核对官网账单，不要重复创建"))
		return
	}
	writeJSON(w, http.StatusOK, row.Checkout)
}

func (s *Server) apiTHCheckout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in thregister.CheckoutRequest
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	if err := in.Validate(); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	id := r.PathValue("id")
	row, err := store.Get(id)
	if err != nil {
		s.thStoreError(w, err)
		return
	}
	// Same selection returns the saved link, never silently creates another
	// Stripe session. Its current price/expiry must be checked on Stripe.
	if row.Checkout != nil && row.Checkout.Kind == in.Kind && row.Checkout.Product == in.Product && thregister.ValidCheckoutURL(row.Checkout.URL) {
		writeJSON(w, http.StatusOK, row.Checkout)
		return
	}
	cfg := s.cfg.Get().TokenHarbor
	proxy := row.ProxyURL
	if proxy == "" {
		proxy = cfg.ProxyURL
	}
	flow, err := thregister.New(thregister.Config{BaseURL: thBaseURL(cfg.BaseURL), ProxyURL: proxy, RequestTimeout: 40 * time.Second})
	if err != nil {
		apiError(w, http.StatusBadRequest, errors.New("无法初始化账号代理"))
		return
	}
	defer flow.Close()
	original := row.Status
	row, err = store.Update(id, func(rw *thsignupstore.Row) error {
		if rw.Status == thsignupstore.Running || rw.Status == thsignupstore.Queued || len(rw.Cookies) == 0 {
			return errors.New("需要已保存且当前未执行任务的账号会话")
		}
		if rw.Checkout != nil {
			return errors.New("已有支付链接或上次结果不确定，已阻止重复创建；请先在官网核对账单")
		}
		// Reserve atomically BEFORE the external mutation. An interrupted call
		// cannot be replayed after restart. Keep this marker on any failure.
		original = rw.Status
		rw.Status = thsignupstore.Running
		rw.Checkout = &thsignupstore.Checkout{Kind: in.Kind, Product: in.Product, CreatedAt: time.Now()}
		return nil
	})
	if err != nil {
		apiError(w, http.StatusConflict, err)
		return
	}
	flow.SeedCookies(thStoredResult(row).Cookies)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	link, callErr := flow.CreateCheckout(ctx, in)
	updated, saveErr := store.Update(id, func(rw *thsignupstore.Row) error {
		rw.Status = original
		// Save refreshed cookies even when the payment endpoint refuses.
		res := thStoredResult(*rw)
		res.Cookies = flow.Session()
		thSaveResult(rw, res)
		if callErr == nil {
			rw.Checkout.URL = link
		}
		return nil
	})
	if saveErr != nil {
		apiError(w, http.StatusInternalServerError, errors.New("支付响应未能持久化，结果不确定；请核对官网账单，不要重试"))
		return
	}
	if callErr != nil {
		apiError(w, http.StatusBadGateway, callErr)
		return
	}
	writeJSON(w, http.StatusOK, updated.Checkout)
}
