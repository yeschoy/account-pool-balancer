package perchtelemetry

import "net/http"

// clientForProxy uses the same transport construction as Perch account calls.
func (r *Reporter) clientForProxy(proxyURL string) (*http.Client, error) {
	return r.proxyClient.HTTPClientForProxy(proxyURL)
}
