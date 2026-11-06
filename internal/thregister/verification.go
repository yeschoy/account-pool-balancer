package thregister

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// SendVerificationEmail performs the dashboard's explicit send action once.
// A sent email is not verification; only alreadyVerified is upstream proof.
func (f *Flow) SendVerificationEmail(ctx context.Context) (alreadyVerified bool, err error) {
	if !HasSession(f.Session()) {
		return false, ErrSessionRejected
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.base()+"/api/me/send-verification-email", nil)
	if err != nil {
		return false, errors.New("邮箱验证发信请求无效")
	}
	f.applyIdentity(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", f.cfg.base())
	req.Header.Set("Referer", f.cfg.base()+"/dashboard")
	client := *f.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return false, errors.New("邮箱验证发信请求未确认完成，未重试")
	}
	defer resp.Body.Close()
	if limited := registrationRateLimit(resp, ""); limited != nil {
		return false, limited
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("邮箱验证发信被拒绝：HTTP %d；未重试", resp.StatusCode)
	}
	var result struct {
		OK              bool `json:"ok"`
		AlreadyVerified bool `json:"alreadyVerified"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil || !result.OK {
		return false, errors.New("邮箱验证发信未获成功确认，未重试")
	}
	return result.AlreadyVerified, nil
}
