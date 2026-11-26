// Package egresstest is the contract any Connectivity driver passes (ADR-0039):
// a host a secret is for gets the real value while the agent holds only the
// placeholder, an echo of the value never reaches the agent, a tunnelled host
// is reached untouched, everything else is refused, and the value never
// appears in what the driver logs.
package egresstest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Fixture is what the contract hands a driver under test. Every upstream host
// in the contract is a name under .test that Dial sends to one local server.
type Fixture struct {
	Dir string
	// Secrets resolves the contract's secrets.
	Secrets core.SecretResolver
	// Dial reaches the contract's upstream for any host.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// UpstreamRoots verifies the upstream's certificate.
	UpstreamRoots *x509.CertPool
	// SystemBundle is the file of roots the agent's trust starts from; it
	// holds the upstream's root, so a tunnelled host verifies.
	SystemBundle string
	// Log is where the driver writes what it logs.
	Log io.Writer
}

// The secret values the contract resolves. The second ends in a newline, as a
// secret held in a file does.
const (
	ValueA = "real-value-aaaa-0123456789"
	ValueB = "real-value-bbbb-9876543210\n"
)

// Run checks the contract against the driver newDriver builds from a Fixture.
func Run(t *testing.T, newDriver func(t *testing.T, f Fixture) core.Connectivity) {
	up := startUpstream(t)
	var logBuf syncBuffer
	f := Fixture{
		Dir: t.TempDir(),
		Secrets: core.SecretResolverFunc(func(name string) ([]byte, error) {
			switch name {
			case "a":
				return []byte(ValueA), nil
			case "b":
				return []byte(ValueB), nil
			}
			return nil, fmt.Errorf("no secret %q", name)
		}),
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, up.addr)
		},
		UpstreamRoots: up.roots,
		SystemBundle:  up.rootFile,
		Log:           &logBuf,
	}
	drv := newDriver(t, f)
	policy := core.EgressPolicy{
		Pass: []string{"pass.test"},
		Inject: []core.InjectRule{
			{Secret: "a", Hosts: []string{"a.test", "a2.test"}, Env: "TOKEN_A"},
			{Secret: "b", Hosts: []string{"b.test"}, Env: "TOKEN_B"},
		},
	}
	eg, err := drv.Open(context.Background(), "agent-1", policy)
	require.NoError(t, err)
	t.Cleanup(func() { _ = drv.Close("agent-1") })
	env := envMap(eg.Env)
	phA, phB := env["TOKEN_A"], env["TOKEN_B"]
	require.NotEmpty(t, phA)
	require.NotEmpty(t, phB)
	assert.NotContains(t, phA, strings.TrimSpace(ValueA))
	client := agentClient(t, eg, env)

	t.Run("a bearer placeholder is filled on its way out", func(t *testing.T) {
		got := up.get(t, client, "https://a.test/echo", "Authorization", "Bearer "+phA)
		assert.Equal(t, "Bearer "+ValueA, got.seen)
		assert.NotContains(t, got.body, ValueA, "the echo reaches the agent scrubbed")
		assert.Contains(t, got.body, phA)
		assert.NotContains(t, got.header, ValueA, "a header echo reaches the agent scrubbed")
	})
	t.Run("every host of the rule gets it", func(t *testing.T) {
		got := up.get(t, client, "https://a2.test/echo", "Authorization", "token "+phA)
		assert.Equal(t, "token "+ValueA, got.seen)
	})
	t.Run("a file's trailing newline is not sent", func(t *testing.T) {
		got := up.get(t, client, "https://b.test/echo", "X-Api-Key", phB)
		assert.Equal(t, strings.TrimSpace(ValueB), got.seen)
		assert.NotContains(t, got.body, strings.TrimSpace(ValueB))
	})
	t.Run("a basic credential is decoded and filled", func(t *testing.T) {
		cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + phA))
		got := up.get(t, client, "https://a.test/echo", "Authorization", "Basic "+cred)
		want := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + ValueA))
		assert.Equal(t, "Basic "+want, got.seen)
	})
	t.Run("a placeholder sent to another secret's host is refused", func(t *testing.T) {
		got := up.get(t, client, "https://a.test/echo", "Authorization", "Bearer "+phB)
		assert.Equal(t, http.StatusForbidden, got.status)
		assert.Empty(t, got.seen, "nothing reached the upstream")
	})
	t.Run("a gzip echo is scrubbed too", func(t *testing.T) {
		got := up.get(t, client, "https://a.test/gzip", "Authorization", "Bearer "+phA)
		assert.Equal(t, http.StatusOK, got.status)
		assert.NotContains(t, got.body, ValueA)
		assert.Contains(t, got.body, phA)
	})
	t.Run("an echo split across writes is scrubbed", func(t *testing.T) {
		got := up.get(t, client, "https://a.test/split", "Authorization", "Bearer "+phA)
		assert.NotContains(t, got.body, ValueA)
		assert.Contains(t, got.body, phA)
	})
	t.Run("a body that cannot be read for an echo is not passed on", func(t *testing.T) {
		got := up.get(t, client, "https://a.test/brotli", "Authorization", "Bearer "+phA)
		assert.Equal(t, http.StatusBadGateway, got.status)
		assert.NotContains(t, got.body, "unreadable-body")
	})
	t.Run("a tunnelled host is reached untouched", func(t *testing.T) {
		got := up.get(t, client, "https://pass.test/echo", "Authorization", "Bearer "+phA)
		assert.Equal(t, "Bearer "+phA, got.seen, "nothing is filled into a tunnel")
		require.NotNil(t, got.tls)
		assert.Equal(t, up.rootName, got.tls.PeerCertificates[0].Issuer.CommonName, "the agent sees the host's own certificate")
	})
	t.Run("any other host is refused", func(t *testing.T) {
		_, err := client.Get("https://other.test/echo")
		require.Error(t, err)
	})
	t.Run("a request that is not a tunnel is refused", func(t *testing.T) {
		c, err := net.Dial("unix", eg.Socket)
		require.NoError(t, err)
		defer func() { _ = c.Close() }()
		_, err = io.WriteString(c, "GET http://a.test:443/echo HTTP/1.1\r\nHost: a.test:443\r\nAuthorization: Bearer "+phA+"\r\n\r\n")
		require.NoError(t, err)
		line := readLine(t, c)
		assert.Contains(t, line, "403")
	})
	t.Run("a port other than 443 is refused", func(t *testing.T) {
		c, err := net.Dial("unix", eg.Socket)
		require.NoError(t, err)
		defer func() { _ = c.Close() }()
		_, err = io.WriteString(c, "CONNECT a.test:8443 HTTP/1.1\r\nHost: a.test:8443\r\n\r\n")
		require.NoError(t, err)
		assert.Contains(t, readLine(t, c), "403")
	})
	t.Run("nothing logged carries a value", func(t *testing.T) {
		logged := logBuf.String()
		assert.NotEmpty(t, logged)
		assert.NotContains(t, logged, ValueA)
		assert.NotContains(t, logged, strings.TrimSpace(ValueB))
		assert.NotContains(t, logged, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+ValueA)))
	})
	t.Run("an agent with an open policy cannot open another", func(t *testing.T) {
		_, err := drv.Open(context.Background(), "agent-1", core.EgressPolicy{})
		assert.Error(t, err)
	})
	t.Run("a policy with no secret sets no trust", func(t *testing.T) {
		eg2, err := drv.Open(context.Background(), "agent-2", core.EgressPolicy{Pass: []string{"pass.test"}})
		require.NoError(t, err)
		defer func() { _ = drv.Close("agent-2") }()
		env2 := envMap(eg2.Env)
		assert.Empty(t, env2["SSL_CERT_FILE"])
		assert.Empty(t, env2["NODE_EXTRA_CA_CERTS"])
		assert.NotEmpty(t, env2["HTTPS_PROXY"])
	})
	t.Run("a policy that does not hold together is refused", func(t *testing.T) {
		for name, p := range map[string]core.EgressPolicy{
			"a host on both lists":   {Pass: []string{"a.test"}, Inject: []core.InjectRule{{Secret: "a", Hosts: []string{"a.test"}, Env: "T"}}},
			"a host for two secrets": {Inject: []core.InjectRule{{Secret: "a", Hosts: []string{"x.test"}, Env: "T"}, {Secret: "b", Hosts: []string{"x.test"}, Env: "U"}}},
			"a wildcard host":        {Pass: []string{"*.test"}},
			"a host with a port":     {Pass: []string{"x.test:443"}},
			"a reserved variable":    {Inject: []core.InjectRule{{Secret: "a", Hosts: []string{"x.test"}, Env: "HTTPS_PROXY"}}},
			"a rule with no host":    {Inject: []core.InjectRule{{Secret: "a", Env: "T"}}},
			"an unknown secret":      {Inject: []core.InjectRule{{Secret: "nope", Hosts: []string{"x.test"}, Env: "T"}}},
		} {
			_, err := drv.Open(context.Background(), "agent-3", p)
			assert.Error(t, err, name)
			_ = drv.Close("agent-3")
		}
	})
	t.Run("after close the socket is gone", func(t *testing.T) {
		require.NoError(t, drv.Close("agent-1"))
		_, err := net.Dial("unix", eg.Socket)
		assert.Error(t, err)
		require.NoError(t, drv.Close("agent-1"), "closing twice is not an error")
	})
}

func readLine(t *testing.T, c net.Conn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b := make([]byte, 64)
	n, _ := io.ReadAtLeast(c, b, 12)
	return string(b[:n])
}

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// agentClient is a client configured from the agent's environment alone, with
// the socket standing in for the route out.
func agentClient(t *testing.T, eg core.Egress, env map[string]string) *http.Client {
	t.Helper()
	proxy, err := url.Parse(env["HTTPS_PROXY"])
	require.NoError(t, err)
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxy),
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", eg.Socket)
			},
			TLSClientConfig:   &tls.Config{RootCAs: trustPool(t, env), MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		},
	}
}

func trustPool(t *testing.T, env map[string]string) *x509.CertPool {
	t.Helper()
	b, err := os.ReadFile(env["SSL_CERT_FILE"])
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(b))
	return pool
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
