package thregister

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
)

// CheckoutRequest is deliberately limited to the requested monthly Agent Pass.
// It authorizes creating a hosted payment page, NOT payment or a plan upgrade.
type CheckoutRequest struct {
	Kind    string `json:"kind"`
	Product string `json:"product"`
}

func (in CheckoutRequest) Validate() error {
	if in.Kind != "pass" || in.Product != "starter_pass_monthly" {
		return errors.New("只支持 Agent Pass 月度单次支付链接，不支持充值、其他套餐或自动续费")
	}
	return nil
}

func ValidCheckoutURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "checkout.stripe.com" &&
		u.User == nil && strings.HasPrefix(u.Path, "/c/pay/") && len(u.Path) > len("/c/pay/") &&
		!strings.ContainsAny(raw, "\r\n\t ")
}

type checkoutResponse struct {
	URL                   string          `json:"checkout_url"`
	Upgraded              bool            `json:"upgraded"`
	Quote                 *CheckoutQuote  `json:"quote"`
	Error                 json.RawMessage `json:"error"`
	RegionWarningRequired bool            `json:"region_warning_required"`
}

// CreateCheckout previews the account first. ONLY a free account's one-time
// Agent Pass quote can proceed to hosted checkout. Existing subscriptions,
// region acknowledgements and "Confirm and pay" upgrade flows hard-stop.
// No stored-card charge, balance payment or capture endpoint is called.
func (f *Flow) CreateCheckout(ctx context.Context, in CheckoutRequest) (string, error) {
	return f.CreateCheckoutGuarded(ctx, in, nil)
}

// CreateCheckoutGuarded calls beforeCreate once after validating the read-only
// preview, immediately before requesting a hosted page. A persistence failure
// must stop creation; preview failures must not leave a pending order marker.
func (f *Flow) CreateCheckoutGuarded(ctx context.Context, in CheckoutRequest, beforeCreate func() error) (string, error) {
	q, err := f.PreviewCheckout(ctx, in)
	if err != nil {
		return "", err
	}
	payload := map[string]any{"plan": in.Product, "period": "monthly", "autoRenew": false, "expectedPayNowCents": int(math.Round(*q.PayNowUSD * 100))}
	if beforeCreate != nil {
		if err := beforeCreate(); err != nil {
			return "", err
		}
	}
	out, err := f.checkoutCall(ctx, payload, "create")
	if err != nil {
		return "", err
	}
	if out.Quote != nil || out.Upgraded {
		return "", checkoutFailure("create", "quote-changed", 200, "", "报价或订阅状态已变化，已停止；不继续确认或重复请求")
	}
	if !ValidCheckoutURL(out.URL) {
		return "", checkoutFailure("create", "invalid-link", 200, "", "支付响应没有可信的 Stripe 托管链接；不会自动重试，请核对官网账单")
	}
	return out.URL, nil
}

// PreviewCheckout issues only preview=true. It neither creates a hosted
// session nor proves that an older pending order is absent.
func (f *Flow) PreviewCheckout(ctx context.Context, in CheckoutRequest) (*CheckoutQuote, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	preview, err := f.checkoutCall(ctx, map[string]any{"plan": in.Product, "period": "monthly", "autoRenew": false, "preview": true}, "preview")
	if err != nil {
		return nil, err
	}
	q := preview.Quote
	if preview.Upgraded || preview.URL != "" || q == nil || q.FromTier != "free" || q.Kind != "tier" || !q.OneTime || q.Period != "monthly" || q.PlanName != "Agent Pass" || q.PayNowUSD == nil {
		return nil, checkoutFailure("preview", "invalid-quote", 200, "", "不是 Free 账号的 Agent Pass 单次报价，已停止；不执行订阅变更或扣款确认")
	}
	cents := math.Round(*q.PayNowUSD * 100)
	if math.IsNaN(cents) || math.IsInf(cents, 0) || cents <= 0 || cents > 199 || math.Abs(*q.PayNowUSD*100-cents) > 0.000001 {
		return nil, checkoutFailure("preview", "price-out-of-range", 200, "", "Agent Pass 报价超出已验证范围，请在官网人工处理")
	}
	return q, nil
}

func (f *Flow) checkoutCall(ctx context.Context, payload map[string]any, stage string) (checkoutResponse, error) {
	var out checkoutResponse
	body, err := json.Marshal(payload)
	if err != nil {
		return out, checkoutFailure(stage, "request-invalid", 0, "", "支付请求格式无效；未提交")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.base()+"/api/billing/subscribe", bytes.NewReader(body))
	if err != nil {
		return out, checkoutFailure(stage, "request-invalid", 0, "", "支付请求地址无效；未提交")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", f.cfg.base())
	req.Header.Set("Referer", f.cfg.base()+"/pricing")
	f.applyIdentity(req)
	client := *f.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		message := "网络失败；未创建订单，可人工重试预检"
		if stage == "create" {
			message = "网络失败，结果不确定；不会自动重试，请先核对官网账单"
		}
		return out, checkoutFailure(stage, "network", 0, "", message)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		message := "预检响应不完整；未创建订单"
		if stage == "create" {
			message = "支付响应不完整，结果不确定；请先核对官网账单"
		}
		return out, checkoutFailure(stage, "response-incomplete", resp.StatusCode, "", message)
	}
	parseErr := json.Unmarshal(raw, &out)
	code := checkoutErrorCode(out.Error)
	errorValue := string(bytes.TrimSpace(out.Error))
	hasBusinessError := errorValue != "" && errorValue != "null" && errorValue != `""`
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || hasBusinessError || out.RegionWarningRequired {
		message := "官网支付请求未成功；已停止，不自动重试"
		if out.RegionWarningRequired {
			code = "region_confirmation_required"
		}
		if known, ok := checkoutErrorMessages[code]; ok {
			message = known
		} else {
			switch resp.StatusCode {
			case http.StatusUnauthorized:
				message = "账号会话失效，请重新登录；不会重新注册"
			case http.StatusForbidden, http.StatusConflict:
				message = "支付被拒绝或需要人工确认；不自动确认或更换出口"
			case http.StatusTooManyRequests:
				message = "支付接口限流；不会自动重试"
			}
		}
		return out, checkoutFailure(stage, "upstream-rejected", resp.StatusCode, code, message)
	}
	if parseErr != nil {
		return out, checkoutFailure(stage, "invalid-json", resp.StatusCode, "", "支付响应无法解析；不会自动重试")
	}
	return out, nil
}
