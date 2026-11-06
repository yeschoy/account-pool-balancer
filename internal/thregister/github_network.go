package thregister

import (
	"net/http/httptrace"
	"sync"
)

// Capture only fixed phase labels, never callback addresses, TLS details or
// error strings. A phase is evidence of progress, not proof of who caused it.
func oauthRequestTrace(usingProxy bool) (*httptrace.ClientTrace, func() string) {
	var mu sync.Mutex
	phase := "网络阶段未确定"
	set := func(value string) { mu.Lock(); phase = value; mu.Unlock() }
	get := func() string { mu.Lock(); defer mu.Unlock(); return phase }
	dns, tcp, connected := "官网 DNS 解析", "官网 TCP 连接", "TCP 已连接，等待 TLS"
	if usingProxy {
		dns, tcp, connected = "代理 DNS 解析", "代理 TCP 连接", "代理协商（SOCKS/CONNECT）"
	}
	return &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { set(dns) },
		ConnectStart: func(string, string) { set(tcp) },
		ConnectDone: func(_, _ string, err error) {
			if err == nil {
				set(connected)
			}
		},
		TLSHandshakeStart: func() { set("TLS 握手") },
		GotConn:           func(httptrace.GotConnInfo) { set("HTTP 连接已就绪") },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				set("发送 HTTP 请求")
			} else {
				set("等待官网响应头")
			}
		},
		GotFirstResponseByte: func() { set("读取官网响应头") },
	}, get
}
