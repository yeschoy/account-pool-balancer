package config

import "strings"

// MistralSignup holds the Mistral registration runner's settings.
//
// 代理凭据放在这里而不是容器环境变量，这样可以在管理页面里改。
// 注意与 mistral.go 里的 Mistral（API 池路由配置）区分开。
type MistralSignup struct {
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
	return m.ProxyUserTemplate != "" && m.ProxyPassword != "" && len(m.ProxySIDs) > 0
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
