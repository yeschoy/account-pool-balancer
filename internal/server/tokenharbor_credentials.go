package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"

	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

// Both pasted formats are supported without treating a receive URL as a password.
func thParseRegisterInput(in thRegisterInput) (email, password, mailbox string, err error) {
	parts := strings.Split(strings.TrimSpace(in.Email), "----")
	if len(parts) > 3 {
		return "", "", "", errors.New("每行最多三段：邮箱----密码----接码页，或邮箱----接码页")
	}
	email = strings.TrimSpace(parts[0])
	if len(parts) == 2 {
		if strings.HasPrefix(strings.TrimSpace(parts[1]), "https://") {
			mailbox = strings.TrimSpace(parts[1])
		} else {
			password = strings.TrimSpace(parts[1])
		}
	} else if len(parts) == 3 {
		password, mailbox = strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	}
	if in.Password != "" {
		password = strings.TrimSpace(in.Password)
	}
	if in.MailboxURL != "" {
		mailbox = strings.TrimSpace(in.MailboxURL)
	}
	if password == "" {
		var b [18]byte
		if _, err = rand.Read(b[:]); err != nil {
			return "", "", "", errors.New("生成注册密码失败")
		}
		password = "Tq7-" + base64.RawURLEncoding.EncodeToString(b[:])
	}
	return
}

func thSaveResult(row *thsignupstore.Row, res thregister.Result) {
	row.APIKey, row.Verified, row.FreeModels = res.APIKey, res.VerifiedEmail, res.FreeModelsEnabled
	row.Cookies = make([]thsignupstore.Cookie, 0, len(res.Cookies))
	for _, c := range res.Cookies {
		row.Cookies = append(row.Cookies, thsignupstore.Cookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly, SameSite: c.SameSite,
		})
	}
}

func thStoredResult(row thsignupstore.Row) thregister.Result {
	res := thregister.Result{Email: row.Email, APIKey: row.APIKey,
		VerifiedEmail: row.Verified, FreeModelsEnabled: row.FreeModels}
	for _, c := range row.Cookies {
		res.Cookies = append(res.Cookies, thregister.NamedCookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly, SameSite: c.SameSite,
		})
	}
	return res
}
