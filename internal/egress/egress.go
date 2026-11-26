// Package egress is pao's credential-injection driver for the Connectivity
// plane (ADR-0039): every outbound call an agent makes leaves through a proxy
// pao runs, which tunnels the hosts the agent may reach untouched, fills a
// credential into its calls to the hosts a secret is for, and refuses
// everything else.
//
// The agent holds only a placeholder. The proxy resolves the real value
// through the Secrets plane, keeps it in memory, and puts it into a request
// header on the way out; an echo of the value in a response is put back to the
// placeholder before the agent reads it. Nothing the proxy logs carries a
// header: a decision is logged by host only.
//
// The proxy is a boundary only if it is the agent's only way out. This
// package serves each agent on a Unix socket; making that socket the only
// route out of the agent's network is the launch wrapper's job.
package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// BridgeAddr is where, inside the agent's own network, the agent reaches the
// proxy. The network holds nothing else, so the address is fixed.
const BridgeAddr = "127.0.0.1:3128"

// The names the trust files and the proxy address are given in. Each runtime
// reads its own, so all are set.
var (
	proxyVars  = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"}
	bundleVars = []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO"}
	extraVars  = []string{"NODE_EXTRA_CA_CERTS"}
)

// systemBundles are where a Linux system keeps its trusted roots, in the order
// Go's own crypto/x509 looks.
var systemBundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

var (
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	envPattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

// Driver serves each agent's egress on its own socket under Dir.
type Driver struct {
	Dir     string
	Secrets core.SecretResolver
	// Log receives one line per decision, by host. Nil is slog.Default.
	Log *slog.Logger
	// Dial reaches an upstream host. Nil is a plain dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// UpstreamRoots verifies the real certificate of a host a secret is
	// filled for. Nil is the system's roots.
	UpstreamRoots *x509.CertPool
	// SystemBundle is the file of roots the agent's trust file starts from,
	// so the hosts it reaches through a tunnel still verify. Empty finds the
	// system's.
	SystemBundle string

	mu   sync.Mutex
	runs map[string]*run
}

var _ core.Connectivity = (*Driver)(nil)

// run is one agent's opened policy.
type run struct {
	agent  string
	dir    string
	ln     net.Listener
	ca     *authority
	pass   map[string]bool
	inject map[string]*rule
	rules  []*rule
	pairs  []pair

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
}

type rule struct {
	secret      string
	placeholder string
	value       []byte
}

// Open validates p, resolves its secrets, and starts serving the agent's
// egress. The agent must not have an open policy.
func (d *Driver) Open(_ context.Context, agent string, p core.EgressPolicy) (core.Egress, error) {
	if !namePattern.MatchString(agent) {
		return core.Egress{}, fmt.Errorf("agent name %q", agent)
	}
	if err := validate(p); err != nil {
		return core.Egress{}, fmt.Errorf("egress policy for %s: %w", agent, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runs == nil {
		d.runs = map[string]*run{}
	}
	if _, ok := d.runs[agent]; ok {
		return core.Egress{}, fmt.Errorf("egress for %s is already open", agent)
	}
	r, env, err := d.open(agent, p)
	if err != nil {
		return core.Egress{}, err
	}
	d.runs[agent] = r
	r.wg.Add(1)
	go d.serve(r)
	return core.Egress{Env: env, Socket: r.ln.Addr().String()}, nil
}

func (d *Driver) open(agent string, p core.EgressPolicy) (r *run, env []string, err error) {
	r = &run{agent: agent, pass: map[string]bool{}, inject: map[string]*rule{}, conns: map[net.Conn]struct{}{}}
	for _, h := range p.Pass {
		r.pass[h] = true
	}
	var injectHosts []string
	for _, ir := range p.Inject {
		value, err := d.Secrets.Resolve(ir.Secret)
		if err == nil {
			value, err = headerValue(value)
		}
		if err != nil {
			r.wipe()
			return nil, nil, fmt.Errorf("secret %q: %w", ir.Secret, err)
		}
		ph, err := newPlaceholder()
		if err != nil {
			r.wipe()
			return nil, nil, err
		}
		ru := &rule{secret: ir.Secret, placeholder: ph, value: value}
		r.rules = append(r.rules, ru)
		r.pairs = append(r.pairs, pair{value: value, placeholder: []byte(ph)})
		for _, h := range ir.Hosts {
			r.inject[h] = ru
			injectHosts = append(injectHosts, h)
		}
		env = append(env, ir.Env+"="+ph)
	}
	defer func() {
		if err != nil {
			r.wipe()
			if r.dir != "" {
				_ = os.RemoveAll(r.dir)
			}
		}
	}()
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return nil, nil, err
	}
	r.dir = filepath.Join(d.Dir, agent)
	if err := os.Mkdir(r.dir, 0o700); err != nil {
		r.dir = ""
		return nil, nil, fmt.Errorf("egress for %s: %w", agent, err)
	}
	for _, v := range proxyVars {
		env = append(env, v+"=http://"+BridgeAddr)
	}
	if len(injectHosts) > 0 {
		if r.ca, err = newAuthority(agent, injectHosts); err != nil {
			return nil, nil, err
		}
		caFile := filepath.Join(r.dir, "ca.pem")
		bundleFile := filepath.Join(r.dir, "bundle.pem")
		roots, err := d.systemRoots()
		if err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(caFile, r.ca.pem, 0o644); err != nil {
			return nil, nil, err
		}
		bundle := append(append(roots, '\n'), r.ca.pem...)
		if err := os.WriteFile(bundleFile, bundle, 0o644); err != nil {
			return nil, nil, err
		}
		for _, v := range bundleVars {
			env = append(env, v+"="+bundleFile)
		}
		for _, v := range extraVars {
			env = append(env, v+"="+caFile)
		}
	}
	if r.ln, err = net.Listen("unix", filepath.Join(r.dir, "proxy.sock")); err != nil {
		return nil, nil, err
	}
	return r, env, nil
}

func (d *Driver) systemRoots() ([]byte, error) {
	if d.SystemBundle != "" {
		return os.ReadFile(d.SystemBundle)
	}
	for _, f := range systemBundles {
		if b, err := os.ReadFile(f); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("no system root bundle found")
}

// Close stops serving the agent's egress, drops every connection it has open,
// and forgets its secrets. Having nothing open is not an error.
func (d *Driver) Close(agent string) error {
	d.mu.Lock()
	r, ok := d.runs[agent]
	delete(d.runs, agent)
	d.mu.Unlock()
	if !ok {
		return nil
	}
	err := r.ln.Close()
	r.mu.Lock()
	for c := range r.conns {
		_ = c.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
	r.wipe()
	if rerr := os.RemoveAll(r.dir); rerr != nil && err == nil {
		err = rerr
	}
	if errors.Is(err, net.ErrClosed) {
		err = nil
	}
	return err
}

// wipe overwrites the run's secret values; the run is not used again.
func (r *run) wipe() {
	for _, ru := range r.rules {
		clear(ru.value)
	}
}

func (d *Driver) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

func (d *Driver) dial(ctx context.Context, addr string) (net.Conn, error) {
	if d.Dial != nil {
		return d.Dial(ctx, "tcp", addr)
	}
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", addr)
}

func (d *Driver) serve(r *run) {
	defer r.wg.Done()
	for {
		c, err := r.ln.Accept()
		if err != nil {
			return
		}
		r.mu.Lock()
		r.conns[c] = struct{}{}
		r.mu.Unlock()
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer func() {
				r.mu.Lock()
				delete(r.conns, c)
				r.mu.Unlock()
				_ = c.Close()
			}()
			d.handle(r, c)
		}()
	}
}

// handle reads the agent's CONNECT and tunnels, intercepts or refuses it.
func (d *Driver) handle(r *run, c net.Conn) {
	br := bufio.NewReader(c)
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	if req.Method != http.MethodConnect {
		d.log().Warn("egress: refused a request that is not a tunnel", "agent", r.agent)
		refuse(c, "only tunnelled https leaves this network")
		return
	}
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		refuse(c, "a tunnel names host:port")
		return
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if port != "443" {
		d.log().Warn("egress: refused a port", "agent", r.agent, "host", host, "port", port)
		refuse(c, "only port 443 leaves this network")
		return
	}
	conn := &bufConn{Conn: c, r: br}
	switch {
	case r.inject[host] != nil:
		d.log().Info("egress: intercepting", "agent", r.agent, "host", host)
		d.intercept(r, conn, host)
	case r.pass[host]:
		d.log().Info("egress: tunnelling", "agent", r.agent, "host", host)
		d.tunnel(conn, host)
	default:
		d.log().Warn("egress: refused a host", "agent", r.agent, "host", host)
		refuse(c, "this host is not reachable from here")
	}
}

func refuse(c net.Conn, why string) {
	_, _ = fmt.Fprintf(c, "HTTP/1.1 403 Forbidden\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s\n", len(why)+1, why)
}

const established = "HTTP/1.1 200 Connection Established\r\n\r\n"

func (d *Driver) tunnel(c net.Conn, host string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	up, err := d.dial(ctx, net.JoinHostPort(host, "443"))
	cancel()
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer func() { _ = up.Close() }()
	if _, err := io.WriteString(c, established); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); _ = up.Close(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); _ = c.Close(); done <- struct{}{} }()
	<-done
	<-done
}

// bufConn reads what the CONNECT's reader already buffered before the
// connection itself.
type bufConn struct {
	net.Conn
	r io.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// headerValue is a resolved value as it can go into a header: a file's
// trailing newline is dropped, and a value that is empty, or that carries a
// control character which could end the header, is refused.
func headerValue(v []byte) ([]byte, error) {
	t := bytes.TrimSpace(v)
	if len(t) == 0 {
		return nil, errors.New("its value is empty")
	}
	for _, c := range t {
		if c < 0x20 || c == 0x7f {
			return nil, errors.New("its value holds a control character")
		}
	}
	out := append([]byte(nil), t...)
	clear(v)
	return out, nil
}

func newPlaceholder() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "pao-placeholder-" + hex.EncodeToString(b), nil
}

func validate(p core.EgressPolicy) error {
	seen := map[string]string{}
	host := func(h, as string) error {
		if !hostPattern.MatchString(h) {
			return fmt.Errorf("host %q is not a lower-case DNS name", h)
		}
		if prev, ok := seen[h]; ok {
			return fmt.Errorf("host %q is both %s and %s", h, prev, as)
		}
		seen[h] = as
		return nil
	}
	for _, h := range p.Pass {
		if err := host(h, "pass"); err != nil {
			return err
		}
	}
	reserved := map[string]bool{}
	for _, v := range append(append(append([]string{}, proxyVars...), bundleVars...), extraVars...) {
		reserved[v] = true
	}
	envs := map[string]bool{}
	for _, ir := range p.Inject {
		if !namePattern.MatchString(ir.Secret) {
			return fmt.Errorf("secret name %q", ir.Secret)
		}
		if !envPattern.MatchString(ir.Env) || reserved[ir.Env] || envs[ir.Env] {
			return fmt.Errorf("variable %q for secret %q is not a free variable name", ir.Env, ir.Secret)
		}
		envs[ir.Env] = true
		if len(ir.Hosts) == 0 {
			return fmt.Errorf("secret %q names no host", ir.Secret)
		}
		for _, h := range ir.Hosts {
			if err := host(h, "inject for "+ir.Secret); err != nil {
				return err
			}
		}
	}
	return nil
}

// intercept ends the agent's TLS with a certificate from the run's authority
// and proxies each request on to the real host with the secret filled in.
func (d *Driver) intercept(r *run, c net.Conn, host string) {
	if _, err := io.WriteString(c, established); err != nil {
		return
	}
	cert, err := r.ca.leaf(host)
	if err != nil {
		d.log().Error("egress: certificate", "agent", r.agent, "host", host, "err", err)
		return
	}
	tc := tls.Server(c, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"http/1.1"},
		MinVersion:   tls.VersionTLS12,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = tc.HandshakeContext(ctx)
	cancel()
	if err != nil {
		return
	}
	h, transport := d.injector(r, host)
	defer transport.CloseIdleConnections()
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          discardLog,
	}
	_ = srv.Serve(newOneConn(tc))
}

// contextT keeps the transport's dial signature readable.
type contextT = context.Context
