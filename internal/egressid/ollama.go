package egressid

import "net/http"

// OllamaUserAgent identifies the gateway honestly, without the downstream
// device's OS, browser build, SDK version or persistent installation ID.
const OllamaUserAgent = "account-pool-balancer/ollama-gateway"

// OllamaClient is a credential-bearing API boundary, not a browser identity.
// The original client, requests and shared Transport remain untouched. Every
// call (including metadata and diagnostics) uses the same small header set;
// unknown headers are private by default. Native body/query bytes are unchanged.
func OllamaClient(client *http.Client) *http.Client {
	out := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if _, ok := transport.(ollamaTransport); !ok {
		out.Transport = ollamaTransport{base: transport}
	}
	out.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &out
}

type ollamaTransport struct{ base http.RoundTripper }

func (t ollamaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Shallow-copy the request and copy only allowed headers. Do not copy
	// arbitrary SDK/device headers just to delete them again on the hot path.
	out := new(http.Request)
	*out = *req
	out.Header = make(http.Header, 6)
	for name, values := range req.Header {
		switch http.CanonicalHeaderKey(name) {
		case "Authorization", "Content-Type", "Accept", "Anthropic-Version", "Anthropic-Beta":
			out.Header[http.CanonicalHeaderKey(name)] = append(out.Header[http.CanonicalHeaderKey(name)], values...)
		}
	}
	out.Header.Set("User-Agent", OllamaUserAgent)
	return t.base.RoundTrip(out)
}
