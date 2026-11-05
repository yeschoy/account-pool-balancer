package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"account-pool-balancer/internal/thregister"
)

const thSignupInterval = time.Minute

func (r *thRegistrar) generation() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pauseSequence
}

// A pause invalidates already queued work. Expiry only permits a new explicit
// request; it never revives work that was waiting when the refusal arrived.
func (r *thRegistrar) checkPaused(generation uint64, checkGeneration bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if wait := time.Until(r.pausedUntil); wait > 0 {
		return &thregister.RateLimitError{RetryAfter: wait, Reason: "此前官网已限制注册，本地任务已暂停"}
	}
	if checkGeneration && generation != r.pauseSequence {
		return errors.New("本任务已因官网注册限制停止；请核对后人工重新发起，不自动恢复")
	}
	return nil
}

func (r *thRegistrar) pause(wait time.Duration) {
	if wait <= 0 {
		wait = 5 * time.Minute
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	until := time.Now().Add(wait)
	if until.After(r.pausedUntil) {
		r.pausedUntil = until
	}
	r.pauseSequence++
}

func (r *thRegistrar) rejectPaused(w http.ResponseWriter) bool {
	err := r.checkPaused(0, false)
	if err == nil {
		return false
	}
	var limited *thregister.RateLimitError
	if errors.As(err, &limited) {
		w.Header().Set("Retry-After", fmt.Sprint(ceilSeconds(limited.RetryAfter)))
	}
	apiError(w, http.StatusTooManyRequests, err)
	return true
}

// Called once before precheck/possible captcha purchase, and again immediately
// before the signup POST. The one global worker slot prevents intervening
// reservations, so the second check does not let a token expire in a queue.
func (r *thRegistrar) awaitSignup(ctx context.Context, reserve bool) error {
	for {
		if err := r.checkPaused(0, false); err != nil {
			return err
		}
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
