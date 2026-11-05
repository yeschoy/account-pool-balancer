package server

import (
	"errors"
	"net/http"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thsignupstore"
)

var errTHReturnIncomplete = errors.New("账号已移出号池，但注册资料恢复失败；请在「已入池」页重试退回，不要重新注册")

// thReturn restores only the original registration, never a guessed email/key
// match. No upstream request or credential creation/revocation is involved.
func (s *Server) thReturn(accountID string) (thsignupstore.Row, error) {
	s.thAdmissionMu.Lock()
	defer s.thAdmissionMu.Unlock()
	store, err := s.thStore()
	if err != nil {
		return thsignupstore.Row{}, errors.New("注册资料读取失败，未退回")
	}
	var rowID string
	for _, row := range store.List() {
		if row.AccountID != accountID {
			continue
		}
		if rowID != "" {
			return thsignupstore.Row{}, errors.New("账号关联了多条注册记录，请先人工核对；未退回")
		}
		rowID = row.ID
	}
	if rowID == "" {
		return thsignupstore.Row{}, errors.New("没有对应的原注册记录，无法恢复会话和接码资料；号池账号未修改")
	}
	poolCleared := false
	row, err := store.Update(rowID, func(rw *thsignupstore.Row) error {
		if rw.Account != accountID || rw.Status != thsignupstore.Imported {
			return errors.New("注册记录状态已变化，请刷新后重试；未退回")
		}
		_, err := s.cfg.Update(-1, func(c *config.Config) error {
			for i, a := range c.Accounts {
				if a.ID != accountID {
					continue
				}
				if a.Provider != config.ProviderTokenHarbor || rw.APIKey == "" || a.RuntimeKey != rw.APIKey {
					return errors.New("号池账号来源或 Key 已变化，不能退回旧注册资料；号池账号未修改")
				}
				c.Accounts = append(c.Accounts[:i], c.Accounts[i+1:]...)
				return nil
			}
			// A previous attempt may have removed the pool account before the
			// encrypted-row save failed. Repeating here only finishes that save.
			return nil
		})
		if err != nil {
			return err
		}
		poolCleared = true
		rw.Status = thsignupstore.Registered
		if !rw.Verified {
			rw.Status = thsignupstore.Unverified
		}
		rw.Account = ""
		rw.Detail = "已退回注册机，原会话、Key 和接码资料已保留"
		return nil
	})
	if err != nil && poolCleared {
		return thsignupstore.Row{}, errTHReturnIncomplete
	}
	return row, err
}

func (s *Server) apiTHReturn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	row, err := s.thReturn(r.PathValue("id"))
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, errTHReturnIncomplete) {
			status = http.StatusInternalServerError
		}
		apiError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, thsignupstore.SummaryOf(row))
}
