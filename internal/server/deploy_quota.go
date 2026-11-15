package server

// deploy_quota.go —— 号池额度查询
//
// 关键设计（2026-11-15 实测）：
//   deploy 的会话只有 **一个 cookie**：hive-portal-production。
//   把它从 AdsPower profile 里提取出来，**在任何机器上纯 HTTP 直连都能用**：
//
//       GET https://ajax-portal.deploy.com/api/organization/{org}/credit
//       Cookie: hive-portal-production=...
//
//   所以额度查询**不需要浏览器，也不需要 CDP 隧道**。
//   隧道是给绑卡这种必须真浏览器的操作用的。
//
// 数据流：
//   注册时（Mac，profile 内）→ 提取 session_cookie → 入库
//   之后（OVH，纯 HTTP）→ 定时用 cookie 查额度
//
// 额度口径：
//   /credit 返回**流水数组**，余额 = 各条 amount 之和。
//   reason: initial（注册赠送）/ bought（充值）/ purchase_promo（满额赠送）。

import (
	"account-pool-balancer/internal/deploysignupstore"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	quotaBase          = "https://ajax-portal.deploy.com"
	quotaOrigin        = "https://deploy.com"
	quotaSessionCookie = "hive-portal-production"
	// 与浏览器一致的 UA。服务端虽未校验，但保持一致没有坏处。
	quotaUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36"
)

// creditRow 是流水的一条。
type creditRow struct {
	ChargedOn string  `json:"charged_on"`
	Amount    string  `json:"amount"` // 上游是字符串，如 "100.00"
	Reason    string  `json:"reason"`
	TaxAmount string  `json:"tax_amount"`
	Status    *string `json:"status"`
}

// quotaResult 是一次查询的结果。
//
// Balance 用 *float64：查不到时是 nil（未知），绝不返回 0 糊弄——
// 0 会被上层当成"这个号没额度了"，与"查询失败"是完全不同的语义。
type quotaResult struct {
	OK        bool        `json:"ok"`
	OrgID     string      `json:"org_id"`
	Balance   *float64    `json:"balance"`
	Rows      []creditRow `json:"rows,omitempty"`
	Error     string      `json:"error,omitempty"`
	CheckedAt time.Time   `json:"checked_at"`
}

// deployCookieGet 用 session cookie 发 GET（纯 HTTP，无浏览器）。
func deployCookieGet(ctx context.Context, cookie, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		quotaBase+path, nil)
	if err != nil {
		return 0, nil, err
	}
	h := req.Header
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("Origin", quotaOrigin)
	h.Set("Referer", "https://deploy.com/")
	h.Set("User-Agent", quotaUserAgent)
	h.Set("X-Forwarded-Proto", "https")
	h.Set("Cookie", quotaSessionCookie+"="+cookie)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, nil
}

// fetchQuota 查一个组织的余额。
func fetchQuota(ctx context.Context, cookie, orgID string) quotaResult {
	res := quotaResult{OrgID: orgID, CheckedAt: time.Now().UTC()}
	if cookie == "" {
		res.Error = "没有 session_cookie"
		return res
	}
	if orgID == "" {
		res.Error = "没有 org_id"
		return res
	}

	status, body, err := deployCookieGet(ctx, cookie,
		"/api/organization/"+orgID+"/credit")
	if err != nil {
		res.Error = "网络错误: " + err.Error()
		return res
	}
	var wrap struct {
		Code    int         `json:"status_code"`
		Message string      `json:"message"`
		Data    []creditRow `json:"data"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		res.Error = "响应无法解析: " + truncate(string(body), 100)
		return res
	}
	if status != http.StatusOK {
		res.Error = fmt.Sprintf("HTTP %d: %s", status, wrap.Message)
		return res
	}

	// 求和。任一 amount 解析失败就把整体标为未知，不要给出错误的总数。
	var total float64
	for _, r := range wrap.Data {
		v, err := strconv.ParseFloat(strings.TrimSpace(r.Amount), 64)
		if err != nil {
			res.Error = "金额解析失败: " + r.Amount
			res.Rows = wrap.Data
			return res
		}
		total += v
	}
	total = float64(int(total*100+0.5)) / 100
	res.OK = true
	res.Balance = &total
	res.Rows = wrap.Data
	return res
}

// sessionAlive 只用 cookie 判断会话是否还有效。
//
// 会话失效 **不代表账号作废**：cookie 会被服务端轮换，
// 正确做法是走隧道重新登录刷新，而不是把账号标成报废。
func sessionAlive(ctx context.Context, cookie string) (bool, string) {
	if cookie == "" {
		return false, "没有 session_cookie"
	}
	status, body, err := deployCookieGet(ctx, cookie, "/api/profile")
	if err != nil {
		return false, "网络错误: " + err.Error()
	}
	if status == http.StatusOK {
		return true, "ok"
	}
	if status == http.StatusUnauthorized {
		return false, "会话失效（401）"
	}
	return false, fmt.Sprintf("HTTP %d: %s", status, truncate(string(body), 80))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------------------------------------------------------------- handlers

// apiDeployCandidateQuota 查单个候选的余额。
func (s *Server) apiDeployCandidateQuota(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	c, gerr := m.store.Get(id)
	if gerr != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "找不到候选"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()

	res := fetchQuota(ctx, c.SessionCookie, c.OrgID)
	if res.OK && res.Balance != nil {
		// 落库：余额与检查时刻，便于列表直接展示
		_, _ = m.store.Update(id, func(x *deploysignupstore.Candidate) error {
			x.CreditBalance = strconv.FormatFloat(*res.Balance, 'f', 2, 64)
			x.QuotaCheckedAt = res.CheckedAt
			if x.Error == "" || strings.Contains(x.Error, "会话失效") {
				x.Error = ""
				x.Detail = ""
			}
			return nil
		})
	} else if strings.Contains(res.Error, "401") {
		// 会话失效：只标记 detail，不把账号判为失败
		_, _ = m.store.Update(id, func(x *deploysignupstore.Candidate) error {
			x.Detail = "session_expired"
			x.QuotaCheckedAt = res.CheckedAt
			return nil
		})
	}
	writeJSON(w, http.StatusOK, res)
}

// apiDeployQuotaRefresh 批量刷新余额。
//
// 纯 HTTP、低成本，可以定时跑。为避免对上游造成压力：
//   - 串行 + 每条之间 300ms
//   - 跳过没有 cookie 的候选
func (s *Server) apiDeployQuotaRefresh(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	list := m.store.List()
	ctx, cancel := context.WithTimeout(r.Context(),
		time.Duration(len(list)*5+60)*time.Second)
	defer cancel()

	type item struct {
		ID      string   `json:"id"`
		Email   string   `json:"email"`
		OK      bool     `json:"ok"`
		Balance *float64 `json:"balance"`
		Error   string   `json:"error,omitempty"`
	}
	out := make([]item, 0, len(list))
	for _, c := range list {
		if c.SessionCookie == "" && c.OrgID == "" {
			continue
		}
		res := fetchQuota(ctx, c.SessionCookie, c.OrgID)
		it := item{ID: c.ID, Email: c.Email, OK: res.OK,
			Balance: res.Balance, Error: res.Error}
		out = append(out, it)

		cid := c.ID
		if res.OK && res.Balance != nil {
			_, _ = m.store.Update(cid, func(x *deploysignupstore.Candidate) error {
				x.CreditBalance = strconv.FormatFloat(*res.Balance, 'f', 2, 64)
				x.QuotaCheckedAt = res.CheckedAt
				return nil
			})
		} else if strings.Contains(res.Error, "401") {
			_, _ = m.store.Update(cid, func(x *deploysignupstore.Candidate) error {
				x.Detail = "session_expired"
				x.QuotaCheckedAt = res.CheckedAt
				return nil
			})
		}
		select {
		case <-ctx.Done():
			writeJSON(w, http.StatusOK, map[string]any{"items": out,
				"error": "超时，已中断"})
			return
		case <-time.After(300 * time.Millisecond):
		}
	}

	var total float64
	var known int
	for _, it := range out {
		if it.OK && it.Balance != nil {
			total += *it.Balance
			known++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":        out,
		"count":        len(out),
		"known":        known,
		"total_credit": total,
	})
}
