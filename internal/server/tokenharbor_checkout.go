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

// Preview diagnoses a saved session/quote without creating or replaying an
// order, and without treating a successful quote as an order-status check.
func (s *Server) apiTHCheckoutPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in thregister.CheckoutRequest
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, errors.New("支付预检请求格式无效"))
		return
	}
	if err := in.Validate(); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, errors.New("注册资料不可读"))
		return
	}
	row, err := store.Get(r.PathValue("id"))
	if err != nil {
		s.thStoreError(w, err)
		return
	}
	if row.Status == thsignupstore.Running || row.Status == thsignupstore.Queued || len(row.Cookies) == 0 {
		apiError(w, http.StatusConflict, errors.New("需要已保存且当前未执行任务的账号会话"))
		return
	}
	cfg := s.cfg.Get().TokenHarbor
	proxy := row.Egress(cfg.ProxyURL)
	flow, err := thregister.New(thregister.Config{BaseURL: thBaseURL(cfg.BaseURL), ProxyURL: proxy, UserAgent: thRowUserAgent(row), Seed: row.ID, BrowserProfile: row.BrowserProfile, EgressRegion: thEgressRegion(cfg, proxy), TLSFingerprint: thTLSFingerprint(cfg), RequestTimeout: 40 * time.Second})
	if err != nil {
		apiError(w, http.StatusBadRequest, errors.New("无法初始化账号代理"))
		return
	}
	defer flow.Close()
	flow.SeedCookies(thStoredResult(row).Cookies)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	quote, callErr := flow.PreviewCheckout(ctx, in)
	latest, err := store.Get(row.ID)
	current := s.cfg.Get().TokenHarbor
	if err != nil || latest.UpdatedAt != row.UpdatedAt || current.BaseURL != cfg.BaseURL || latest.Egress(current.ProxyURL) != proxy {
		apiError(w, http.StatusConflict, errors.New("预检期间账号或出口变化，请重新检查；未创建订单"))
		return
	}
	if thSessionChanged(thStoredResult(row).Cookies, flow.Session()) {
		_, err = store.Update(row.ID, func(rw *thsignupstore.Row) error {
			current := s.cfg.Get().TokenHarbor
			if rw.UpdatedAt != row.UpdatedAt || current.BaseURL != cfg.BaseURL || rw.Egress(current.ProxyURL) != proxy {
				return errors.New("账号来源变化")
			}
			res := thStoredResult(*rw)
			res.Cookies = flow.Session()
			thSaveResult(rw, res)
			return nil
		})
		if err != nil {
			apiError(w, http.StatusConflict, errors.New("预检会话更新未能保存；未创建订单，请刷新检查"))
			return
		}
	}
	if callErr != nil {
		s.thCheckoutFailure(row.Email, proxy, callErr)
		apiError(w, http.StatusBadGateway, callErr)
		return
	}
	s.thLive(row.Email, "支付报价预检", "会话及报价可用；未创建订单，不代表没有旧待处理订单", proxy, "info")
	writeJSON(w, http.StatusOK, map[string]any{"quote": quote, "createsOrder": false, "provesNoPendingOrder": false})
}

func (s *Server) thCheckoutFailure(email, proxy string, err error) {
	detail := "本地状态检查或持久化失败；已停止，不自动重试"
	if failure := thregister.CheckoutFailureOf(err); failure != nil {
		detail = failure.Error()
	}
	s.thLive(email, "支付链接失败", detail, proxy, "error")
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
	flow, err := thregister.New(thregister.Config{BaseURL: thBaseURL(cfg.BaseURL), ProxyURL: proxy, UserAgent: thRowUserAgent(row), Seed: row.ID, BrowserProfile: row.BrowserProfile, EgressRegion: thEgressRegion(cfg, proxy), TLSFingerprint: thTLSFingerprint(cfg), RequestTimeout: 40 * time.Second})
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
			message := "上次支付链接结果不确定"
			if rw.Checkout.Failure != nil {
				message += "；首次失败：" + rw.Checkout.Failure.Error()
			}
			return errors.New(message + "；请先在官网核对账单，确认未付款且无待处理订单后，勾选“已核对账单”再获取链接")
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
	reservedForCreate := false
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
			reservedForCreate = true
			s.thLive(row.Email, "支付链接", "已通过报价预检，开始获取托管链接；不会付款", proxy, "info")
		}
		return err
	})
	if callErr != nil {
		s.thCheckoutFailure(row.Email, proxy, callErr)
	}
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
			rw.Checkout.Failure = nil
		} else if reservedForCreate && rw.Checkout != nil {
			rw.Checkout.Failure = thregister.CheckoutFailureOf(callErr)
		}
		return nil
	})
	if saveErr != nil {
		s.thLive(row.Email, "支付链接失败", "支付响应未能持久化，结果不确定；请核对官网账单，不要重试", proxy, "error")
		apiError(w, http.StatusInternalServerError, errors.New("支付响应未能持久化，结果不确定；请核对官网账单，不要重试"))
		return
	}
	if callErr != nil {
		apiError(w, http.StatusBadGateway, callErr)
		return
	}
	s.thLive(row.Email, "支付链接", "托管链接已保存；尚未付款", proxy, "success")
	writeJSON(w, http.StatusOK, updated.Checkout)
}
