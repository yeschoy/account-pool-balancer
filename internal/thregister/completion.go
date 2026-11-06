package thregister

import (
	"context"
	"crypto/sha256"
	"errors"
)

// CompletionOrder is persisted per account, not reselected on resume/failure.
type CompletionOrder string

const (
	KeyThenVerify CompletionOrder = "key-then-verify"
	VerifyThenKey CompletionOrder = "verify-then-key"
)

func (o CompletionOrder) valid() bool {
	return o == "" || o == KeyThenVerify || o == VerifyThenKey
}

// CompletionOrderFor uses an independent salt from the browser profile. Row
// IDs are crypto-random; persisting this result fixes the choice across runs.
func CompletionOrderFor(seed string) CompletionOrder {
	sum := sha256.Sum256([]byte("completion-order\x00" + seed))
	if sum[0]&1 == 1 {
		return VerifyThenKey
	}
	return KeyThenVerify
}

func (f *Flow) completeAPIKey(ctx context.Context, res *Result) error {
	if res.APIKey == "" && res.APIKeyCreationAttempted {
		return errors.New("上次 API Key 创建结果不确定；请先在官网核对，禁止自动重复创建")
	}
	// Validate the saved login even when res already has a one-time key. Never
	// replace that key on a resume or infer account absence from a refusal.
	if err := f.probeAPIKey(ctx); err != nil {
		return err
	}
	if res.APIKey != "" {
		return nil
	}
	res.APIKeyCreationAttempted = true
	if err := f.checkpoint(res); err != nil {
		return err
	}
	f.cfg.step("创建 API Key", "")
	key, err := f.createAPIKey(ctx)
	if err != nil {
		return err
	}
	res.APIKey = key
	return f.checkpoint(res)
}

func (f *Flow) completeVerification(ctx context.Context, res *Result, allowSend bool) error {
	if res.VerifiedEmail || f.cfg.Mailbox == nil {
		return nil
	}
	res.MailTimedOut = false
	if res.OAuthComplete && allowSend && !res.VerificationMailAttempted {
		res.VerificationMailAttempted = true
		if err := f.checkpoint(res); err != nil {
			return err
		}
		f.cfg.step("发送验证邮件", "提交一次官网发信动作；不代表已验证")
		alreadyVerified, err := f.SendVerificationEmail(ctx)
		if err != nil {
			return err
		}
		res.VerifiedEmail = alreadyVerified
		if alreadyVerified {
			return nil
		}
	}
	f.cfg.step("等验证邮件", "最长 "+f.cfg.mailTimeout().String())
	link, err := f.waitVerificationLink(ctx, res.Email)
	if errors.Is(err, ErrMailboxRejected) {
		return err
	}
	if err != nil || link == "" {
		res.MailTimedOut = true
		f.cfg.step("等验证邮件", "超时；保留当前顺序和凭证，可手动继续")
		return nil
	}
	if err := f.verifyEmail(ctx, link); err != nil {
		// http.Client errors can contain the full one-time verification URL.
		// Never return that credential into row details or live logs.
		f.cfg.step("邮箱验证", "未获成功确认；未重新发信")
		return errors.New("邮箱验证未获成功确认；保留会话，请人工核对验证状态")
	}
	res.VerifiedEmail = true
	f.cfg.step("邮箱验证", "成功")
	return nil
}
