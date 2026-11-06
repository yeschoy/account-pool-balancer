package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

func (s *Server) apiTHResendVerification(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	cfg := s.cfg.Get().TokenHarbor
	var original thsignupstore.Status
	row, err := store.Update(r.PathValue("id"), func(rw *thsignupstore.Row) error {
		if (rw.Status != thsignupstore.Unverified && rw.Status != thsignupstore.Registered) || rw.Verified || !thregister.HasSession(thStoredResult(*rw).Cookies) {
			return errors.New("只有已注册、邮箱未验证且已保存登录会话的空闲账号可以重发验证码")
		}
		original = rw.Status
		rw.Status = thsignupstore.Running
		return nil
	})
	if err != nil {
		if errors.Is(err, thsignupstore.ErrNotFound) {
			s.thStoreError(w, err)
		} else {
			apiError(w, http.StatusConflict, err)
		}
		return
	}
	proxy := row.Egress(cfg.ProxyURL)
	flow, callErr := thregister.New(thregister.Config{
		BaseURL: thBaseURL(cfg.BaseURL), ProxyURL: proxy, Seed: row.ID,
		EgressRegion: thEgressRegion(cfg, proxy), UserAgent: thRowUserAgent(row), RequestTimeout: 40 * time.Second,
	})
	if callErr == nil {
		defer flow.Close()
		flow.SeedCookies(thStoredResult(row).Cookies)
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		s.thLive(row.Email, "重发验证码", "请求官网重新发送验证邮件", proxy, "info")
		callErr = flow.ResendVerification(ctx, func() error {
			current := s.cfg.Get().TokenHarbor
			latest, err := store.Get(row.ID)
			if err != nil || latest.UpdatedAt != row.UpdatedAt || current.BaseURL != cfg.BaseURL || latest.Egress(current.ProxyURL) != proxy {
				return errors.New("发送前账号或出口变化，未发送验证码，请重新操作")
			}
			return nil
		})
	} else {
		callErr = errors.New("无法初始化账号代理，未发送验证码")
	}
	updated, err := store.Update(row.ID, func(rw *thsignupstore.Row) error {
		if rw.UpdatedAt != row.UpdatedAt || rw.Status != thsignupstore.Running {
			return errors.New("重发期间账号记录变化，请核对收件箱；未自动重试")
		}
		rw.Status = original
		current := s.cfg.Get().TokenHarbor
		if flow != nil && current.BaseURL == cfg.BaseURL && rw.Egress(current.ProxyURL) == proxy {
			res := thStoredResult(*rw)
			res.Cookies = flow.Session()
			thSaveResult(rw, res)
		}
		if callErr == nil {
			rw.Detail = "验证码已重发，请稍后点击补验证"
		}
		return nil
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, errors.New("重发结果未能保存，请核对收件箱；不要重复点击"))
		return
	}
	if callErr != nil {
		s.thLive(row.Email, "重发验证码", callErr.Error(), proxy, "error")
		apiError(w, http.StatusBadGateway, callErr)
		return
	}
	s.thLive(row.Email, "重发验证码", "官网已确认发送，请稍后补验证", proxy, "success")
	writeJSON(w, http.StatusOK, thsignupstore.SummaryOf(updated))
}
