package perch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// The surface gate.
//
// Perch refuses a model call with perch_surface_required unless the first
// system message begins with Perch's own operator preamble — roughly the first
// 200 characters are compared, and text appended after that is ignored. The
// requirement is on the CONTENT of the message, not on the ticket or the
// User-Agent (a call without a ticket and with a generic User-Agent still
// passes when the preamble is there).
//
// That text is Perch's, and this program does not redistribute it. It is
// derived at runtime from a Perch client the operator already has installed —
// the same text that client sends — and cached in the data directory. When no
// source can be found the pool refuses to serve requests rather than failing
// every call upstream with a confusing 403.
const (
	// GatePrefixMin is the length the preamble must reach. The gate compares
	// about the first 200 characters; 200 is the measured minimum.
	GatePrefixMin = 200
	// GatePrefixAnchor is how a Perch client's persona prompt is recognised
	// inside its bundle.
	GatePrefixAnchor = "You are Saffron"
	// GatePrefixEnv lets an operator supply the text directly.
	GatePrefixEnv = "ODB_PERCH_SYSTEM_PREFIX"
)

// ErrNoGatePrefix: no prefix is configured and none could be derived.
var ErrNoGatePrefix = errors.New("Perch 的 surface gate 前缀未配置：本服务不随仓库分发 Perch 的 persona 文本，请从本机已安装的 perchai-cli 自动派生，或在「Perch 设置」里填写")

// GatePrefixSources lists where a prefix may come from, most specific first.
//
// A CLI bundle keeps the persona prompt as a template literal, so it is read as
// text rather than executed: nothing from the client is run.
func GatePrefixSources() []string {
	var out []string
	if p := strings.TrimSpace(os.Getenv(GatePrefixEnv)); p != "" {
		out = append(out, "env:"+GatePrefixEnv)
	}
	if p := strings.TrimSpace(os.Getenv("PERCH_CLI_PATH")); p != "" {
		out = append(out, p)
	}
	if runtime.GOOS == "windows" {
		if app := os.Getenv("APPDATA"); app != "" {
			out = append(out, filepath.Join(app, "npm", "node_modules", "perchai-cli", "dist", "perch.mjs"))
		}
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{
		filepath.Join(home, ".local", "lib", "node_modules"),
		filepath.Join(home, ".npm-global", "lib", "node_modules"),
		"/usr/local/lib/node_modules",
		"/opt/homebrew/lib/node_modules",
		filepath.Join(home, ".nvm", "versions"),
	} {
		out = append(out, filepath.Join(dir, "perchai-cli", "dist", "perch.mjs"))
	}
	// `npm prefix -g` is the last resort: npm may live anywhere.
	if npm, err := exec.LookPath("npm"); err == nil {
		_ = npm
		if prefix, err := exec.Command("npm", "prefix", "-g").Output(); err == nil {
			out = append(out, filepath.Join(strings.TrimSpace(string(prefix)), "node_modules", "perchai-cli", "dist", "perch.mjs"))
		}
	}
	return out
}

// DeriveGatePrefix reads the preamble out of an installed Perch client. It
// returns "" when no client could be read; callers treat that as "unknown"
// rather than an error so a setup page can still render.
func DeriveGatePrefix() (string, string) {
	for _, src := range GatePrefixSources() {
		if strings.HasPrefix(src, "env:") {
			if p := strings.TrimSpace(os.Getenv(GatePrefixEnv)); len(p) >= GatePrefixMin {
				return p, src
			}
			continue
		}
		if p := prefixFromFile(src); p != "" {
			return p, src
		}
	}
	return "", ""
}

// prefixFromFile pulls the persona prompt out of a CLI bundle.
func prefixFromFile(path string) string {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() > 256<<20 {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := string(raw)
	anchor := strings.Index(text, GatePrefixAnchor)
	if anchor < 0 {
		return ""
	}
	// walk back to the opening backtick of the template literal
	start := -1
	for i := anchor - 1; i >= 0 && i > anchor-8; i-- {
		if text[i] == '`' {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	// walk forward to the closing backtick (skipping an escaped one)
	end := -1
	for i := anchor; i < len(text) && i < anchor+(64<<10); i++ {
		if text[i] == '`' && (i == 0 || text[i-1] != '\\') {
			end = i
			break
		}
	}
	if end < 0 {
		return ""
	}
	body := text[start+1 : end]
	body = strings.TrimPrefix(body, "\n")
	body = strings.ReplaceAll(body, `\u2014`, "\u2014")
	body = strings.ReplaceAll(body, `\u2019`, "\u2019")
	body = strings.ReplaceAll(body, `\u2018`, "\u2018")
	body = strings.ReplaceAll(body, `\u201c`, "\u201c")
	body = strings.ReplaceAll(body, `\u201d`, "\u201d")
	body = strings.ReplaceAll(body, `\'`, "'")
	body = strings.ReplaceAll(body, `\"`, `"`)
	body = strings.ReplaceAll(body, `\n`, "\n")

	// The persona prompt is the opening paragraphs; the gate only compares its
	// head, and stopping at the first template hole keeps this a stable slice.
	if cut := strings.Index(body, "${"); cut > 0 {
		body = body[:cut]
	}
	body = strings.TrimSpace(body)
	if len([]rune(body)) < GatePrefixMin {
		return ""
	}
	return body
}

// CheckGatePrefix reports whether s satisfies the gate (its length is what the
// gate measures; the content is Perch's to know).
func CheckGatePrefix(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return ErrNoGatePrefix
	}
	if len([]rune(s)) < GatePrefixMin {
		return errors.New("surface gate 前缀太短：Perch 会比较首条 system 消息的开头约 200 个字符，至少需要 200 个")
	}
	if !strings.Contains(s, GatePrefixAnchor) {
		// not fatal (Perch may change its wording), but worth surfacing
		return errors.New("surface gate 前缀看起来不像是 Perch 的 persona 文本（缺少 '" + GatePrefixAnchor + "' 开头），请核对")
	}
	return nil
}

// ValidateGatePrefix is CheckGatePrefix under a name the settings layer uses.
func ValidateGatePrefix(s string) error { return CheckGatePrefix(s) }

// CodeOf names a model-call failure's Perch error code ("" when unknown).
func CodeOf(err error) string {
	var me *ModelError
	if errors.As(err, &me) {
		return me.Code
	}
	return ""
}
