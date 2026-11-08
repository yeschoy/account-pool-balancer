package config

import "strings"

// MistralSignup holds the Mistral registration runner's settings.
//
// 代理凭据放在这里而不是容器环境变量，这样可以在管理页面里改。
// 注意与 mistral.go 里的 Mistral（API 池路由配置）区分开。
type MistralSignup struct {
	// ProxyURLs 是完整代理 URL 列表，一行一个。
	// 格式：协议://用户:密码@IP:端口（socks5 / socks5h / http / https 均可）。
	// zooproxy 也是这个格式，无需区分。
	ProxyURLs []string `json:"proxyUrls"`
	// CardLines 是银行卡列表，一行一张。
	// 格式：卡号----有效期----CVV，例如 4633433883347213----05/28----913。
	// 只保存原始行，解析交给 mistralcards 包，避免多处重复实现。
	CardLines []string `json:"cardLines"`
	// MailboxProxy 可选，用于走 iCloud HME 轮询；留空表示直连。
	MailboxProxy string `json:"mailboxProxy"`
	// WorkerPath / WorkerPython 覆盖镜像内默认值；留空用默认。
	WorkerPath   string `json:"workerPath"`
	WorkerPython string `json:"workerPython"`
	// Concurrency 是同时进行的注册数上限（当前实现固定为 1）。
	Concurrency int `json:"concurrency"`

	// ---- 已废弃字段 ----
	// 旧的 zooproxy 模板模式。保留这些字段只为让严格解析（ReadJSON 的
	// DisallowUnknownFields）能读入旧 config.json —— 写盘时会归一化为空值，
	// 逻辑上不再使用。删除它们会让线上配置变成 "corrupt"。
	DeprecatedProxyUserTemplate string   `json:"proxyUserTemplate,omitempty"`
	DeprecatedProxyPassword     string   `json:"proxyPassword,omitempty"`
	DeprecatedProxySIDs         []string `json:"proxySids,omitempty"`
	DeprecatedProxyHostPort     string   `json:"proxyHostPort,omitempty"`
	DeprecatedRegion            string   `json:"region,omitempty"`
}

// DefaultMistralSignup 只给非敏感默认值。
func DefaultMistralSignup() MistralSignup {
	return MistralSignup{Concurrency: 1}
}

// normalizeMistralSignup 补齐空字段。
func (c *Config) normalizeMistralSignup() error {
	m := &c.MistralSignup
	def := DefaultMistralSignup()
	if m.Concurrency <= 0 {
		m.Concurrency = def.Concurrency
	}
	// 旧模板字段已废弃：读入后立即清空，下次写盘就不再出现
	m.DeprecatedProxyUserTemplate = ""
	m.DeprecatedProxyPassword = ""
	m.DeprecatedProxySIDs = nil
	m.DeprecatedProxyHostPort = ""
	m.DeprecatedRegion = ""
	return nil
}

// HasProxy reports whether proxies are configured.
func (m MistralSignup) HasProxy() bool { return len(m.ProxyURLs) > 0 }

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

// AllProxies 返回去掉空行后的代理列表。
func (m MistralSignup) AllProxies() []string {
	out := make([]string, 0, len(m.ProxyURLs))
	for _, u := range m.ProxyURLs {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// HasCards reports whether any card line is configured.
func (m MistralSignup) HasCards() bool { return len(m.AllCards()) > 0 }

// AllCards 返回去掉空行后的银行卡原始行列表。
// 只做去空白，不解析格式；解析见 mistralcards.ParseLines。
func (m MistralSignup) AllCards() []string {
	out := make([]string, 0, len(m.CardLines))
	for _, c := range m.CardLines {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// PickCardByIndex 按序号轮转取卡：第 n 个候选固定用第 n%len 张卡，
// 与代理的轮转策略保持一致。
//
// 注意：这里只做轮转分配，不保证卡不被跨账号复用。真正的一卡一号约束
// 由卡池的使用记录实现；如需强制唯一，请在调用方记录已用卡。
func (m MistralSignup) PickCardByIndex(n int) string {
	all := m.AllCards()
	if len(all) == 0 {
		return ""
	}
	if n < 0 {
		n = 0
	}
	return all[n%len(all)]
}
