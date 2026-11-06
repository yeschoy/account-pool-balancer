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
	var in struct {
		thregister.CheckoutRequest
		ConfirmNoPendingOrder bool `json:"confirmNoPendingOrder"`
	}
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
	proxy := row.Egress(cfg.ProxyURL)
	flow, err := thregister.New(thregister.Config{BaseURL: thBaseURL(cfg.BaseURL), ProxyURL: proxy, UserAgent: thRowUserAgent(row), RequestTimeout: 40 * time.Second})
	if err != nil {
		apiError(w, http.StatusBadRequest, errors.New("无法初始化账号代理"))
		return
	}
	defer flow.Close()
	original := row.Status
	row, err = store.Update(id, func(rw *thsignupstore.Row) error {
		current := s.cfg.Get().TokenHarbor
		if rw.UpdatedAt != row.UpdatedAt || current.BaseURL != cfg.BaseURL || rw.Egress(current.ProxyURL) != proxy {
			return errors.New("创建支付链接前账号或出口变化，请重新操作")
		}
		if rw.Status == thsignupstore.Running || rw.Status == thsignupstore.Queued || len(rw.Cookies) == 0 {
			return errors.New("需要已保存且当前未执行任务的账号会话")
		}
		if rw.Checkout != nil && !in.ConfirmNoPendingOrder {
			return errors.New("上次支付链接结果不确定；请先在官网核对账单，确认未付款且无待处理订单后，勾选“已核对账单”再获取链接")
		}
		if rw.Checkout != nil && rw.Checkout.URL != "" {
			return errors.New("已有支付链接，不能覆盖；请读取已保存链接并在官网核对状态")
		}
		// Lock the row during preview, but do not claim an order was submitted.
		// Legacy uncertain markers remain until an explicitly confirmed recovery
		// reaches the creation boundary; batch requests never confirm recovery.
		original = rw.Status
		rw.Status = thsignupstore.Running
		return nil
	})
	if err != nil {
		apiError(w, http.StatusConflict, err)
		return
	}
	flow.SeedCookies(thStoredResult(row).Cookies)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	link, callErr := flow.CreateCheckoutGuarded(ctx, in.CheckoutRequest, func() error {
		reserved, err := store.Update(id, func(rw *thsignupstore.Row) error {
			current := s.cfg.Get().TokenHarbor
			if rw.UpdatedAt != row.UpdatedAt || rw.Status != thsignupstore.Running || current.BaseURL != cfg.BaseURL || rw.Egress(current.ProxyURL) != proxy {
				return errors.New("报价期间账号或出口变化，未创建支付链接；请重新操作")
			}
			// Persist BEFORE the external mutation. Any failure after this point
			// remains uncertain, including an interrupted call or process restart.
			rw.Checkout = &thsignupstore.Checkout{Kind: in.Kind, Product: in.Product, CreatedAt: time.Now()}
			return nil
		})
		if err == nil {
			row = reserved
		}
		return err
	})
	updated, saveErr := store.Update(id, func(rw *thsignupstore.Row) error {
		if rw.UpdatedAt != row.UpdatedAt || rw.Status != thsignupstore.Running {
			return errors.New("支付操作期间账号记录变化")
		}
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
