package config

import "strings"

// MistralSignup holds the Mistral registration runner's settings.
//
// 代理凭据放在这里而不是容器环境变量，这样可以在管理页面里改。
// 注意与 mistral.go 里的 Mistral（API 池路由配置）区分开。
type MistralSignup struct {
	// ProxyURLs 是完整代理 URL 列表，格式 socks5://user:pass@ip:port
	// （也支持 http/https）。填了就优先使用，忽略下面的模板字段。
	ProxyURLs []string `json:"proxyUrls"`
	// ProxyUserTemplate 是 zooproxy 用户名模板，{region} 与 {sid} 会在每次
	// 注册时替换。
	ProxyUserTemplate string `json:"proxyUserTemplate"`
	// ProxyPassword 是 zooproxy 密码。
	ProxyPassword string `json:"proxyPassword"`
	// ProxySIDs 是粘性会话列表，每次注册挑一个。
	ProxySIDs []string `json:"proxySids"`
	// ProxyHostPort 是代理入口，例如 us-eu.zooproxy.com:5000。
	ProxyHostPort string `json:"proxyHostPort"`
	// Region 传给 {region} 的地区代码。
	Region string `json:"region"`
	// MailboxProxy 可选，用于走 iCloud HME 轮询；留空表示直连。
	MailboxProxy string `json:"mailboxProxy"`
	// WorkerPath / WorkerPython 覆盖镜像内默认值；留空用默认。
	WorkerPath   string `json:"workerPath"`
	WorkerPython string `json:"workerPython"`
	// Concurrency 是同时进行的注册数上限（当前实现固定为 1）。
	Concurrency int `json:"concurrency"`
}

// DefaultMistralSignup 只给非敏感默认值：代理入口与地区。
func DefaultMistralSignup() MistralSignup {
	return MistralSignup{
		ProxyHostPort: "us-eu.zooproxy.com:5000",
		Region:        "CA",
		Concurrency:   1,
	}
}

// normalizeMistralSignup 补齐空字段。
func (c *Config) normalizeMistralSignup() error {
	m := &c.MistralSignup
	def := DefaultMistralSignup()
	if m.ProxyHostPort == "" {
		m.ProxyHostPort = def.ProxyHostPort
	}
	if m.Region == "" {
		m.Region = def.Region
	}
	if m.Concurrency <= 0 {
		m.Concurrency = def.Concurrency
	}
	return nil
}

// HasProxy reports whether enough proxy settings exist to attempt a signup.
func (m MistralSignup) HasProxy() bool {
	if len(m.ProxyURLs) > 0 {
		return true
	}
	return m.ProxyUserTemplate != "" && m.ProxyPassword != "" && len(m.ProxySIDs) > 0
}

// ProxyMode 返回当前使用的是哪一种配置方式，便于界面提示。
func (m MistralSignup) ProxyMode() string {
	if len(m.ProxyURLs) > 0 {
		return "urls"
	}
	if m.HasProxy() {
		return "template"
	}
	return "none"
}

// PickProxyByIndex 按序号轮转取代理：第 n 个候选固定用第 n%len 个代理，
// 代理可以重复使用。
func (m MistralSignup) PickProxyByIndex(n int) string {
	all := m.AllProxies()
	if len(all) == 0 {
		return ""
	}
	if n < 0 {
		n = 0
	}
	return all[n%len(all)]
}

// PickProxy 返回这次注册要用的代理 URL（兼容旧调用）。
func (m MistralSignup) PickProxy(sid string) string {
	if len(m.ProxyURLs) > 0 {
		return m.ProxyURLs[0]
	}
	return m.ProxyURLFor(sid)
}

// AllProxies 扁平化返回所有可用代理（URL 模式直接返回列表）。
func (m MistralSignup) AllProxies() []string {
	if len(m.ProxyURLs) > 0 {
		out := make([]string, 0, len(m.ProxyURLs))
		for _, u := range m.ProxyURLs {
			if u = strings.TrimSpace(u); u != "" {
				out = append(out, u)
			}
		}
		return out
	}
	out := make([]string, 0, len(m.ProxySIDs))
	for _, sid := range m.ProxySIDs {
		if u := m.ProxyURLFor(sid); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// ProxyURLFor builds one zooproxy URL for the given sid.
func (m MistralSignup) ProxyURLFor(sid string) string {
	if !m.HasProxy() {
		return ""
	}
	user := strings.ReplaceAll(m.ProxyUserTemplate, "{region}", m.Region)
	user = strings.ReplaceAll(user, "{sid}", sid)
	return "http://" + user + ":" + m.ProxyPassword + "@" + m.ProxyHostPort
}
