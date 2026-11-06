package server

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"

	"account-pool-balancer/internal/thmailbox"
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

func thParseGitHubInput(in thRegisterInput) (thsignupstore.Row, error) {
	login, password, secret, mailbox := strings.TrimSpace(in.Login), in.Password, in.TOTPSecret, strings.TrimSpace(in.MailboxURL)
	parts := strings.Split(strings.TrimSpace(in.Email), "----")
	if len(parts) == 4 {
		login, password, secret, mailbox = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2]), strings.TrimSpace(parts[3])
	} else if len(parts) != 1 {
		return thsignupstore.Row{}, errors.New("GitHub 格式：账号----密码----2FA密钥----邮箱接码地址")
	} else if login == "" {
		login = strings.TrimSpace(in.Email)
	}
	login = strings.ToLower(login)
	if err := thregister.ValidateGitHub(login, password, secret); err != nil {
		return thsignupstore.Row{}, err
	}
	if mailbox == "" {
		return thsignupstore.Row{}, errors.New("GitHub 账号必须提供邮箱接码地址，用于 TokenHarbor 邮箱验证")
	}
	if err := thmailbox.Validate(mailbox); err != nil {
		return thsignupstore.Row{}, err
	}
	for _, proxy := range []string{in.ProxyURL, in.GitHubProxyURL} {
		if err := thsignupstore.ValidateProxy(proxy); err != nil {
			return thsignupstore.Row{}, err
		}
	}
	if in.SessionProxyURL != nil {
		if err := thsignupstore.ValidateProxy(*in.SessionProxyURL); err != nil {
			return thsignupstore.Row{}, err
		}
	}
	return thsignupstore.Row{Email: "github:" + login, Password: password, MailboxURL: mailbox,
		ProxyURL: strings.TrimSpace(in.ProxyURL), GitHub: &thsignupstore.GitHubAuth{
			Login: login, TOTPSecret: strings.ToUpper(strings.ReplaceAll(secret, " ", "")), ProxyURL: strings.TrimSpace(in.GitHubProxyURL),
		}}, nil
}

func thSaveResult(row *thsignupstore.Row, res thregister.Result) {
	if row.GitHub != nil {
		if strings.Contains(res.Email, "@") {
			row.Email = res.Email
		}
		row.GitHub.OAuthComplete = row.GitHub.OAuthComplete || res.OAuthComplete
		if res.GitHubToken != "" {
			row.GitHub.ProviderToken = res.GitHubToken
		}
		if res.GitHubRefreshToken != "" {
			row.GitHub.ProviderRefreshToken = res.GitHubRefreshToken
		}
		if res.GitHubCookies != nil {
			row.GitHub.Cookies = thStoreCookies(res.GitHubCookies)
		}
	}
	row.APIKey, row.Verified, row.FreeModels = res.APIKey, res.VerifiedEmail, res.FreeModelsEnabled
	row.Cookies = thStoreCookies(res.Cookies)
}

func thStoreCookies(cookies []thregister.NamedCookie) []thsignupstore.Cookie {
	out := make([]thsignupstore.Cookie, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, thsignupstore.Cookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly, SameSite: c.SameSite,
		})
	}
	return out
}

// thRowUserAgent returns the User-Agent override for a saved row's tokenharbor.ai
// requests.
//
// It is deliberately always empty. A GitHub row used to override every
// site request with the OAuth product name, which meant plan checks, checkout
// previews and subscription reads announced "account-pool-balancer/tokenharbor-oauth"
// on the site whose session they carried — a string no browser sends. The OAuth
// identity now stays on the authorization hops (see RunGitHub); the site sees
// the same browser identity as a password registration.
func thRowUserAgent(row thsignupstore.Row) string {
	return ""
}

func thStoredResult(row thsignupstore.Row) thregister.Result {
	res := thregister.Result{Email: row.Email, APIKey: row.APIKey,
		VerifiedEmail: row.Verified, FreeModelsEnabled: row.FreeModels}
	if row.GitHub != nil {
		res.OAuthComplete = row.GitHub.OAuthComplete
		res.GitHubToken, res.GitHubRefreshToken = row.GitHub.ProviderToken, row.GitHub.ProviderRefreshToken
		for _, c := range row.GitHub.Cookies {
			res.GitHubCookies = append(res.GitHubCookies, thregister.NamedCookie{
				Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path, Expires: c.Expires,
				Secure: c.Secure, HTTPOnly: c.HTTPOnly, SameSite: c.SameSite,
			})
		}
	}
	for _, c := range row.Cookies {
		res.Cookies = append(res.Cookies, thregister.NamedCookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly, SameSite: c.SameSite,
		})
	}
	return res
}
