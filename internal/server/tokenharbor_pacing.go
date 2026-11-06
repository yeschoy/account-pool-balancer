package server

import (
	"context"
	"errors"
	"time"
)

const thSignupInterval = time.Minute

// Called once before precheck/possible captcha purchase, and again immediately
// before the signup POST. The one global worker slot prevents intervening
// reservations, so the second check does not let a token expire in a queue.
func (r *thRegistrar) awaitSignup(ctx context.Context, reserve bool) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.srv.thStopping:
			return errors.New("服务正在停止，未提交注册")
		default:
		}
		r.mu.Lock()
		wait := time.Until(r.nextSignup)
		if wait <= 0 {
			if reserve {
				interval := r.signupInterval
				if interval <= 0 {
					interval = thSignupInterval
				}
				r.nextSignup = time.Now().Add(interval)
			}
			r.mu.Unlock()
			return nil
		}
		r.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-r.srv.thStopping:
			timer.Stop()
			return errors.New("服务正在停止，未提交注册")
		}
	}
}
