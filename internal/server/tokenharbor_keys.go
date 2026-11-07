package server

import (
	"errors"
	"net/http"
	"strings"
)

// apiTHRowKeys only reveals an already saved key on an explicit admin request.
// It never contacts TokenHarbor, creates a key or changes registration progress.
func (s *Server) apiTHRowKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, errors.New("注册记录读取失败"))
		return
	}
	row, err := store.Get(r.PathValue("id"))
	if err != nil {
		apiError(w, http.StatusNotFound, errors.New("注册记录不存在"))
		return
	}
	if strings.TrimSpace(row.APIKey) == "" {
		apiError(w, http.StatusConflict, errors.New("此记录尚未保存 API Key；不会自动创建或替换密钥"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"runtimeKey": row.APIKey})
}
