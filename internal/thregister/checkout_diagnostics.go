package thregister

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CheckoutQuote contains only the already-validated Free -> Agent Pass quote.
// It is not a checkout session and is never proof of an absent pending order.
type CheckoutQuote struct {
	Kind      string   `json:"kind"`
	FromTier  string   `json:"fromTier"`
	OneTime   bool     `json:"oneTime"`
	Period    string   `json:"period"`
	PlanName  string   `json:"planName"`
	PayNowUSD *float64 `json:"payNowUsd"`
}

// CheckoutFailure is a bounded, credential-free diagnostic. No raw response,
// URL, Cookie, header or unrecognized upstream error value is retained.
type CheckoutFailure struct {
	Stage      string    `json:"stage"`
	Category   string    `json:"category"`
	HTTPStatus int       `json:"httpStatus,omitempty"`
	Code       string    `json:"code,omitempty"`
	Message    string    `json:"message"`
	Uncertain  bool      `json:"uncertain"`
	FailedAt   time.Time `json:"failedAt"`
}

func (e *CheckoutFailure) Error() string {
	stage := "支付链接创建"
	if e.Stage == "preview" {
		stage = "报价预检"
	}
	details := []string{}
	if e.HTTPStatus > 0 {
		details = append(details, fmt.Sprintf("HTTP %d", e.HTTPStatus))
	}
	if e.Code != "" {
		details = append(details, e.Code)
	}
	suffix := ""
	if len(details) > 0 {
		suffix = "（" + strings.Join(details, "，") + "）"
	}
	return stage + "：" + e.Message + suffix
}
func checkoutFailure(stage, category string, status int, code, message string) *CheckoutFailure {
	return &CheckoutFailure{Stage: stage, Category: category, HTTPStatus: status, Code: code, Message: message, Uncertain: stage == "create", FailedAt: time.Now()}
}
func CheckoutFailureOf(err error) *CheckoutFailure {
	var failure *CheckoutFailure
	if !errors.As(err, &failure) || failure == nil {
		return nil
	}
	copy := *failure
	return &copy
}

var checkoutErrorMessages = map[string]string{
	"checkout_unavailable_no_stripe_key": "官网支付服务暂不可用，请联系官网支持",
	"unknown_plan":                       "官网已不支持此套餐，请人工核对",
	"stripe_session_failed":              "官网未能取得支付服务响应；请先核对官网账单",
	"discount_unavailable":               "官网无法应用优惠，请人工核对报价与账单",
	"quote_changed":                      "官网报价已变化；已停止，不继续确认或重复请求",
	"already_subscribed":                 "账号已有此套餐或更高套餐；不创建升级或扣款请求",
	"card_subscription_active":           "已有银行卡续费订阅，请在官网管理",
	"already_on_card_subscription":       "已有银行卡续费订阅，请在官网管理",
	"forbidden_origin":                   "官网拒绝请求来源，请刷新并人工核对",
	"card_payments_paused":               "官网暂停银行卡支付，请稍后人工处理",
	"payments_disabled":                  "官网暂停购买，请稍后人工处理",
	"rate_limited":                       "支付接口限流；不会自动重试",
	"balance_pass_active":                "现有套餐来自余额购买；不会改用余额扣款或升级",
	"not_an_upgrade":                     "不是新购 Free 账号；不会进行套餐变更",
	"already_yearly":                     "账号已有年付套餐，请在官网处理",
	"subscription_not_linked":            "官网订阅关联异常，请联系官网支持",
	"subscription_has_no_item":           "官网订阅项目缺失，请联系官网支持",
	"upgrade_failed":                     "官网订阅变更失败；不继续确认或重试",
	"upgrade_charged_not_applied":        "官网报告已扣款但套餐未生效，请立即联系官网支持，勿重试",
	"payment_incomplete":                 "官网报告支付未完成，请人工核对账单",
	"debit_failed":                       "官网报告扣款失败；不继续扣款或重试",
	"grant_failed":                       "官网报告套餐未生效，请联系官网支持",
	"finish_signup":                      "账号尚未完成官网资料，请人工完成；不会重新注册",
	"region_blocked":                     "官网拒绝当前地区；不会切换出口",
	"region_confirmation_required":       "官网要求人工确认地区；本服务不会代为确认",
}

func checkoutErrorCode(raw json.RawMessage) string {
	var code string
	if json.Unmarshal(raw, &code) != nil {
		var obj struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(raw, &obj) == nil {
			code = obj.Code
		}
	}
	if _, ok := checkoutErrorMessages[code]; ok {
		return code
	}
	return ""
}
