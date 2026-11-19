package server

import (
	"compress/flate"
	"compress/gzip"
	"errors"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// factoryAcceptEncoding is droid's own codec list. It is advertised because the
// gateway decodes every one of them; a compressed body must never reach a
// parser that expects JSON or SSE.
const factoryAcceptEncoding = "gzip, deflate, br, zstd"

var errFactoryEncoding = errors.New("unsupported upstream content-encoding")

// decodeFactoryBody wraps body so the caller always sees the decoded stream,
// whatever codec the upstream picked. The transport disables automatic gzip, so
// this is the only decode path for Factory responses.
//
// Content-Encoding may list codecs applied in order; they are undone in reverse
// order and the chain owns exactly one close.
func decodeFactoryBody(body io.ReadCloser, contentEncoding string) (io.ReadCloser, error) {
	encodings := strings.Split(strings.ToLower(strings.TrimSpace(contentEncoding)), ",")
	var current io.ReadCloser = body
	for i := len(encodings) - 1; i >= 0; i-- {
		switch strings.TrimSpace(encodings[i]) {
		case "", "identity":
			continue
		case "gzip", "x-gzip":
			gz, err := gzip.NewReader(current)
			if err != nil {
				closeQuiet(current)
				return nil, err
			}
			current = &decodeChainReader{r: gz, underlying: current}
		case "deflate":
			current = &decodeChainReader{r: flate.NewReader(current), underlying: current}
		case "br":
			current = &decodeChainReader{r: brotli.NewReader(current), underlying: current}
		case "zstd":
			dec, err := zstd.NewReader(current, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
			if err != nil {
				closeQuiet(current)
				return nil, err
			}
			current = &decodeChainReader{r: dec.IOReadCloser(), underlying: current}
		default:
			closeQuiet(current)
			return nil, errFactoryEncoding
		}
	}
	return &decodeChainReader{r: current, underlying: current}, nil
}

// decodeChainReader reports one Close for the whole chain: the decoder first,
// then the transport body it wraps.
type decodeChainReader struct {
	r          io.Reader
	underlying io.Closer
}

func (d *decodeChainReader) Read(p []byte) (int, error) { return d.r.Read(p) }

func (d *decodeChainReader) Close() error { return d.underlying.Close() }

func closeQuiet(c io.Closer) {
	if c != nil {
		_ = c.Close()
	}
}
