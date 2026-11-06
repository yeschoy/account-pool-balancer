package server

import (
	"context"
	"errors"
)

// checkSignup guards site/captcha work and the final submission without adding
// a local delay. The registrar still enforces task and global worker limits.
func (r *thRegistrar) checkSignup(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.srv.thStopping:
		return errors.New("服务正在停止，未提交注册")
	default:
		return nil
	}
}
