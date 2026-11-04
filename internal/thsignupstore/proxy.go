package thsignupstore

import (
	"errors"
	"net/url"
	"strings"
)

// parseProxy validates an egress address and returns its credential-free
// label ("socks5://host:port"). It accepts the schemes the rest of the program
// accepts and refuses anything with a path, query or fragment, because those
// would silently change where the request goes.
func parseProxy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return "", errors.New("代理地址含空白字符")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.Port() == "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("代理地址无效")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", errors.New("代理只支持 http、https、socks5")
	}
	return u.Scheme + "://" + u.Host, nil
}

// ValidateProxy is the exported form for the admin API.
func ValidateProxy(raw string) error {
	_, err := parseProxy(raw)
	return err
}
