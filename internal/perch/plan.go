package perch

// Plan status: the plan gate a hosted-model turn has to pass.
//
// A model call is refused with perch_surface_required unless the account has a
// plan. `/api/perch-terminal/cli-plan` is where the published clients read and
// choose it, and it is the only endpoint that reports a *banned* account
// plainly: the account route still answers 200 for a banned account, so a check
// built on it cannot see the ban at all. Reading the status is a plain GET and
// costs no allowance. See docs/perch-desktop-and-ban-analysis.md.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// CLIPlanPath is the plan gate.
const CLIPlanPath = "/api/perch-terminal/cli-plan"

// PlanStatus is what the gate says about one account.
type PlanStatus struct {
	// OK is the response's own success flag.
	OK bool
	// PlanRequired: the account has not chosen a plan, so it cannot start a
	// hosted-model turn until its owner picks one.
	PlanRequired bool
	// AlreadyChosen is set when a choice was submitted and the server says it
	// had already been made.
	AlreadyChosen bool
	// NoticeVersion is the version of the Starter notice a choice must echo.
	NoticeVersion string
	// ErrorCode is the structured refusal code ("banned", "not_authenticated",
	// "notice_version_mismatch", "oauth_required", "capacity_reached",
	// "select_failed", "unavailable").
	ErrorCode string
	// Error is the server's own text.
	Error string
	// Status is the HTTP status.
	Status int
}

// Banned reports an explicit ban verdict.
//
// This is the only reliable ban signal available to a client: a banned account
// still answers the account route with 200, and its ticket request fails with
// not_authenticated rather than banned. The reason text also distinguishes
// "using Perch through another app or tool" from "using multiple accounts on
// the free tier".
func (p PlanStatus) Banned() bool { return strings.EqualFold(p.ErrorCode, "banned") }

// Detail renders the server's refusal for an operator.
func (p PlanStatus) Detail() string {
	if p.Error != "" {
		return fmt.Sprintf("%s (%s)", p.Error, p.ErrorCode)
	}
	if p.ErrorCode != "" {
		return p.ErrorCode
	}
	return fmt.Sprintf("HTTP %d", p.Status)
}

// planResponse is the plan endpoint's answer in both directions. A successful
// GET carries planRequired and the Starter notice; a refusal carries ok=false
// with an error and errorCode.
type planResponse struct {
	OK            bool   `json:"ok"`
	PlanRequired  bool   `json:"planRequired"`
	AlreadyChosen bool   `json:"alreadyChosen"`
	Error         string `json:"error"`
	ErrorCode     string `json:"errorCode"`
	Starter       struct {
		NoticeVersion string `json:"noticeVersion"`
	} `json:"starter"`
}

// PlanStatus reads the plan gate for one account. It is a GET: no allowance is
// spent and no state changes.
func (c *Client) PlanStatus(ctx context.Context, appURL, accessToken string) (PlanStatus, error) {
	out := PlanStatus{}
	body, status, err := c.planRequest(ctx, http.MethodGet, appURL, accessToken, nil)
	out.Status = status
	if err != nil {
		return out, err
	}
	var js planResponse
	if len(bytes.TrimSpace(body)) > 0 {
		_ = json.Unmarshal(body, &js)
	}
	out.OK = js.OK
	out.PlanRequired = js.PlanRequired
	out.AlreadyChosen = js.AlreadyChosen
	out.Error = js.Error
	out.ErrorCode = js.ErrorCode
	out.NoticeVersion = js.Starter.NoticeVersion
	return out, nil
}

// ChooseStarter picks the Starter plan. It is a write, so it is only used when
// an operator asks for it; on a banned account it is what surfaces the ban.
func (c *Client) ChooseStarter(ctx context.Context, appURL, accessToken, noticeVersion string) (PlanStatus, error) {
	in := map[string]any{"plan": "starter", "noticeVersion": noticeVersion}
	out := PlanStatus{}
	body, status, err := c.planRequest(ctx, http.MethodPost, appURL, accessToken, in)
	out.Status = status
	if err != nil {
		return out, err
	}
	var js planResponse
	if len(bytes.TrimSpace(body)) > 0 {
		_ = json.Unmarshal(body, &js)
	}
	out.OK = js.OK
	out.PlanRequired = js.PlanRequired
	out.AlreadyChosen = js.AlreadyChosen
	out.Error = js.Error
	out.ErrorCode = js.ErrorCode
	out.NoticeVersion = js.Starter.NoticeVersion
	return out, nil
}

// planRequest performs one plan-gate call. A non-2xx answer is not an error
// here: the refusal body is the answer (that is where `banned` lives), so it is
// returned for the caller to interpret.
func (c *Client) planRequest(ctx context.Context, method, appURL, accessToken string, in any) ([]byte, int, error) {
	base := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if base == "" {
		base = AppURL
	}
	var rdr io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+CLIPlanPath, rdr)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", UserAgent)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	return body, res.StatusCode, nil
}
