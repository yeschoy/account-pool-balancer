package server

// altapi_quota.go —— altapi 号池余额查询
//
// 关键设计（2026-11-15 实测）：
//   altapi.ai 的余额只能从控制面读，而且必须带 **session cookie**：
//
//       GET https://altapi.ai/api/status      → quota_per_unit（500000）
//       GET https://altapi.ai/api/user/self   → quota / used_quota / request_count
//       Cookie: session=...
//
//   页面的三个统计数字与接口字段是精确对应关系：
//
//       Current balance    = quota      / quota_per_unit
//       Consumption        = used_quota / quota_per_unit
//       Number of Requests = request_count
//
//   推理 Key（Authorization: Bearer）**读不到余额**：/api/user/self 会返回
//   `{"message":"无权进行此操作，access token 无效"}`。用 Key 只能读到已花掉的钱：
//   GET /dashboard/billing/usage → total_usage（美分）。
//
// 语义边界（与 deploy_quota.go 一致）：
//   查不到就等于 nil（未知），绝不返回 0 —— 0 会被上层当成"这个号没额度了"。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"account-pool-balancer/internal/altapisignupstore"
	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/newapiconsole"
)

// altapiQuotaUA 现在只在 newapiconsole 里定义一份；这里保留别名给
// 没有迁移的调用点，避免同一份 UA 出现两个字符串。
const altapiQuotaUA = newapiconsole.UserAgent

// ALTAPIUIDRequired 说明缺少 uid 时为什么读不了余额。new-api 不会从 session
// 里替调用方补全 New-Api-User，所以这是硬前置条件而不是提示性字段。
const ALTAPIUIDRequired = "该账号缺 uid：altapi 的 /api/user/self 要求 New-Api-User 等于登录用户 id" +
	"（填 -1 会返回“与登录用户不匹配”）。注册时自动写入，手工添加的号请填 uid"

// altapiQuotaResult 是一次余额查询的结果。Balance 为 nil 表示未知，不是 0。
type altapiQuotaResult struct {
	OK           bool      `json:"ok"`
	UserID       string    `json:"userId,omitempty"`
	Username     string    `json:"username,omitempty"`
	Group        string    `json:"group,omitempty"`
	Quota        float64   `json:"quota"`
	UsedQuota    float64   `json:"usedQuota"`
	RequestCount int64     `json:"requestCount"`
	QuotaPerUnit float64   `json:"quotaPerUnit"`
	BalanceUSD   *float64  `json:"balanceUsd"`
	UsedUSD      *float64  `json:"usedUsd"`
	Source       string    `json:"source"` // session | key | none
	Error        string    `json:"error,omitempty"`
	Hint         string    `json:"hint,omitempty"`
	CheckedAt    time.Time `json:"checkedAt"`
}

// altapiFetchQuota 是核心读取：一次 /api/status 拿单价，一次 /api/user/self
// 拿账号行。凭据规则（session + New-Api-User 必须匹配）全部交给
// newapiconsole，调用点不再自己拼 header。
//
// 语义边界：读不到就是未知（BalanceUSD 为 nil），绝不返回 0。
func altapiFetchQuota(ctx context.Context, origin, session, apiKey, uid, proxyURL string) altapiQuotaResult {
	res := altapiQuotaResult{CheckedAt: time.Now(), Source: "none"}
	cl := newapiconsole.New(newapiconsole.Request{
		Origin: origin, Session: session, UID: uid, Bearer: apiKey, ProxyURL: proxyURL,
	})

	// 1) 单价：站点自己的 /api/status 优先于配置兜底。
	unit := 500000.0
	if st, err := cl.Status(ctx); err == nil && st.QuotaPerUnit > 0 {
		unit = st.QuotaPerUnit
	}
	res.QuotaPerUnit = unit

	// 2) 没有会话：Key 只能读已花掉的钱，余额未知。
	if newapiconsole.SessionValue(session) == "" {
		res.Source = "none"
		res.Hint = "未绑定 session cookie：推理 Key 读不到余额；在充值页登录后把 session 粘到候选里即可"
		if strings.TrimSpace(apiKey) != "" {
			if spent, err := cl.SpentUSD(ctx); err == nil {
				used := newapiconsole.Round4(spent)
				res.UsedUSD = &used
				res.Source = "key"
				res.OK = true
				res.Hint = "Key 只能读到已用金额；剩余余额需要 session cookie"
			}
		}
		return res
	}

	// 3) 有会话但 uid 未知：new-api 不会从 session 补全，读不了。
	if newapiconsole.NormalizeUID(uid) == "" {
		res.Error = ALTAPIUIDRequired
		res.Hint = "uid 在登录响应的 data.id 里；注册机会自动落盘"
		return res
	}

	self, err := cl.Self(ctx)
	if err != nil {
		res.Error = err.Error()
		switch {
		case errors.Is(err, newapiconsole.ErrNoUID):
			res.Hint = "uid 在登录响应的 data.id 里；注册机会自动落盘"
		case newapiconsole.IsThrottled(err):
			res.Hint = "Cloudflare/网关限速，稍后重试"
		case strings.Contains(err.Error(), "access token 无效"), strings.Contains(err.Error(), "无权"):
			res.Hint = "session cookie 已失效，重新登录后复制新的 session"
		default:
			res.Hint = "读取失败，余额未知（不是 0）"
		}
		return res
	}

	res.OK = true
	res.Source = "session"
	res.UserID = fmt.Sprintf("%d", self.ID)
	res.Username = self.Username
	res.Group = self.Group
	res.Quota = self.Quota
	res.UsedQuota = self.UsedQuota
	res.RequestCount = self.RequestCount
	bal := newapiconsole.BalanceUSD(self.Quota, unit)
	used := newapiconsole.BalanceUSD(self.UsedQuota, unit)
	res.BalanceUSD = &bal
	res.UsedUSD = &used
	return res
}

// apiALTAPICandidateQuota 查单个候选的余额，并把结果写回候选。
func (s *Server) apiALTAPICandidateQuota(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	c, err := m.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	st := m.store.Settings()
	origin := config.ALTAPIOrigin(st.BaseURL + "/v1")
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	res := altapiFetchQuota(ctx, origin, c.SessionCookie, c.APIKey, c.AccountID, c.ProxyURL)
	if res.OK && res.BalanceUSD != nil {
		_, _ = m.store.Update(id, func(cc *altapisignupstore.Candidate) error {
			cc.Quota = res.Quota
			cc.UsedQuota = res.UsedQuota
			cc.QuotaPerUnit = res.QuotaPerUnit
			cc.BalanceUSD = *res.BalanceUSD
			cc.QuotaCheckedAt = res.CheckedAt
			if res.Group != "" {
				cc.Group = res.Group
			}
			if res.UserID != "" {
				cc.AccountID = res.UserID
			}
			return nil
		})
	}
	writeJSON(w, http.StatusOK, res)
}

// apiALTAPIQuotaRefresh 批量刷新所有已拿到 Key 的候选余额。
func (s *Server) apiALTAPIQuotaRefresh(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// ids 为空表示全部；传了只刷选中项（前端多选后点「刷新额度」）。
	var in struct {
		IDs []string `json:"ids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	want := map[string]bool{}
	for _, id := range in.IDs {
		want[id] = true
	}
	var list []altapisignupstore.Candidate
	for _, c := range m.store.List() {
		if len(want) > 0 && !want[c.ID] {
			continue
		}
		list = append(list, c)
	}
	st := m.store.Settings()
	origin := config.ALTAPIOrigin(st.BaseURL + "/v1")
	ctx, cancel := context.WithTimeout(r.Context(),
		time.Duration(len(list)*5+60)*time.Second)
	defer cancel()

	type item struct {
		ID      string   `json:"id"`
		Email   string   `json:"email"`
		OK      bool     `json:"ok"`
		Balance *float64 `json:"balanceUsd"`
		Source  string   `json:"source"`
		Error   string   `json:"error,omitempty"`
	}
	out := make([]item, 0, len(list))
	var total float64
	var known, skipped int
	for _, c := range list {
		// 没有会话也没有 Key 的候选读不到任何额度：余额只在 /api/* 控制面可读，
		// 而控制面要求 session。计入 skipped 而不是当作失败，前端才能说清
		// 「有几个是因为没凭据跳过」。
		if c.SessionCookie == "" && c.APIKey == "" {
			skipped++
			continue
		}
		res := altapiFetchQuota(ctx, origin, c.SessionCookie, c.APIKey, c.AccountID, c.ProxyURL)
		it := item{ID: c.ID, Email: c.Email, OK: res.OK, Balance: res.BalanceUSD,
			Source: res.Source, Error: res.Error}
		if res.OK && res.BalanceUSD != nil {
			total += *res.BalanceUSD
			known++
			_, _ = m.store.Update(c.ID, func(cc *altapisignupstore.Candidate) error {
				cc.Quota = res.Quota
				cc.UsedQuota = res.UsedQuota
				cc.QuotaPerUnit = res.QuotaPerUnit
				cc.BalanceUSD = *res.BalanceUSD
				cc.QuotaCheckedAt = res.CheckedAt
				return nil
			})
		}
		out = append(out, it)
		// 登录面/控制面共用同 IP 预算，连击会触发 CF 429
		select {
		case <-ctx.Done():
		case <-time.After(1200 * time.Millisecond):
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": out, "totalBalanceUsd": round2(total), "known": known,
		"skipped": skipped,
	})
}

// apiALTAPIProxyTest 探测一个代理的出口 IP，用于分配代理前确认可用。
func (s *Server) apiALTAPIProxyTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ProxyURL string `json:"proxyUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org?format=json", nil)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	probe := newapiconsole.New(newapiconsole.Request{Origin: "https://api.ipify.org", ProxyURL: in.ProxyURL})
	resp, err := probe.HTTP.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	writeJSON(w, http.StatusOK, map[string]any{"ok": resp.StatusCode == 200, "body": string(body)})
}
