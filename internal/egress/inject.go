package egress

import (
	"compress/gzip"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
)

// injector proxies the agent's requests for host on to the real host, with the
// run's secret for host filled into their headers.
func (d *Driver) injector(r *run, host string) (http.Handler, *http.Transport) {
	ru := r.inject[host]
	transport := &http.Transport{
		DialContext: func(ctx contextT, _, _ string) (net.Conn, error) {
			return d.dial(ctx, net.JoinHostPort(host, "443"))
		},
		TLSClientConfig:    &tls.Config{RootCAs: d.UpstreamRoots, ServerName: host, MinVersion: tls.VersionTLS12},
		DisableCompression: true,
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "https"
			pr.Out.URL.Host = host
			pr.Out.Host = host
			fill(pr.Out.Header, ru)
			// An echo can be scrubbed only from a body that can be read.
			pr.Out.Header.Set("Accept-Encoding", "identity")
		},
		Transport:      transport,
		ModifyResponse: func(resp *http.Response) error { return scrub(resp, r.pairs) },
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			d.log().Warn("egress: upstream failed", "agent", r.agent, "host", host, "err", err)
			w.WriteHeader(http.StatusBadGateway)
		},
		ErrorLog: discardLog,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if foreign(req.Header, r, ru) {
			d.log().Warn("egress: refused another secret's placeholder", "agent", r.agent, "host", host)
			http.Error(w, "this placeholder is not for this host", http.StatusForbidden)
			return
		}
		rp.ServeHTTP(w, req)
	}), transport
}

// fill replaces the rule's placeholder with its value in every header, and in
// a Basic authorization's decoded user:password.
func fill(h http.Header, ru *rule) {
	ph := ru.placeholder
	for k, vs := range h {
		for i, v := range vs {
			if strings.EqualFold(k, "Authorization") {
				if dec, ok := basic(v); ok && strings.Contains(dec, ph) {
					vs[i] = "Basic " + base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(dec, ph, string(ru.value))))
					continue
				}
			}
			vs[i] = strings.ReplaceAll(v, ph, string(ru.value))
		}
	}
}

// foreign reports whether a header carries a placeholder of a rule other than
// the host's own.
func foreign(h http.Header, r *run, own *rule) bool {
	for _, vs := range h {
		for _, v := range vs {
			dec, _ := basic(v)
			for _, ru := range r.rules {
				if ru != own && (strings.Contains(v, ru.placeholder) || strings.Contains(dec, ru.placeholder)) {
					return true
				}
			}
		}
	}
	return false
}

func basic(v string) (string, bool) {
	if len(v) < 6 || !strings.EqualFold(v[:6], "basic ") {
		return "", false
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v[6:]))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// scrub puts every secret value echoed in a response back to its placeholder,
// in the headers and in the body as it streams.
func scrub(resp *http.Response, pairs []pair) error {
	for _, vs := range resp.Header {
		for i, v := range vs {
			for _, p := range pairs {
				v = strings.ReplaceAll(v, string(p.value), string(p.placeholder))
			}
			vs[i] = v
		}
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		return nil
	}
	switch enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("a gzip response that does not read: %w", err)
		}
		resp.Body = &gzipBody{Reader: zr, src: resp.Body}
		resp.Header.Del("Content-Encoding")
	default:
		// Fail closed: an echo in a body that cannot be read cannot be scrubbed.
		return fmt.Errorf("a %q response cannot be checked for an echoed secret", enc)
	}
	resp.Body = newScrubber(resp.Body, pairs)
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
	return nil
}

type gzipBody struct {
	*gzip.Reader
	src io.Closer
}

func (g *gzipBody) Close() error {
	_ = g.Reader.Close()
	return g.src.Close()
}

var discardLog = log.New(io.Discard, "", 0)

// oneConn is a listener that hands out one connection and then waits for it to
// close, so an http.Server can serve a single intercepted connection.
type oneConn struct {
	c    net.Conn
	once sync.Once
	gone sync.Once
	done chan struct{}
}

func newOneConn(c net.Conn) *oneConn { return &oneConn{c: c, done: make(chan struct{})} }

func (l *oneConn) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = &notifyConn{Conn: l.c, l: l} })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConn) Close() error {
	l.gone.Do(func() { close(l.done) })
	return nil
}

func (l *oneConn) Addr() net.Addr { return l.c.LocalAddr() }

type notifyConn struct {
	net.Conn
	l *oneConn
}

func (n *notifyConn) Close() error {
	err := n.Conn.Close()
	_ = n.l.Close()
	return err
}
