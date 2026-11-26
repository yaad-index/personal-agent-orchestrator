package egresstest

import (
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// upstream is one TLS server standing in for every .test host, under its own
// root. It records the credential each request arrived with and echoes it.
type upstream struct {
	addr     string
	roots    *x509.CertPool
	rootFile string
	rootName string

	mu   sync.Mutex
	seen string
}

type result struct {
	status int
	seen   string
	body   string
	header string
	tls    *tls.ConnectionState
}

func startUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{rootName: "egresstest upstream root"}
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	rootTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: u.rootName},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)
	require.NoError(t, err)
	root, err := x509.ParseCertificate(rootDER)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "upstream"},
		DNSNames:  []string{"a.test", "a2.test", "b.test", "pass.test", "other.test"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, root, &leafKey.PublicKey, rootKey)
	require.NoError(t, err)

	u.roots = x509.NewCertPool()
	u.roots.AddCert(root)
	u.rootFile = filepath.Join(t.TempDir(), "roots.pem")
	require.NoError(t, os.WriteFile(u.rootFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}), 0o600))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	u.addr = ln.Addr().String()
	srv := &http.Server{
		Handler:           http.HandlerFunc(u.serve),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}, MinVersion: tls.VersionTLS12},
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.Close() })
	return u
}

func (u *upstream) serve(w http.ResponseWriter, r *http.Request) {
	cred := r.Header.Get("Authorization")
	if cred == "" {
		cred = r.Header.Get("X-Api-Key")
	}
	u.mu.Lock()
	u.seen = cred
	u.mu.Unlock()
	switch r.URL.Path {
	case "/echo":
		w.Header().Set("X-Echo", cred)
		_, _ = io.WriteString(w, "you sent: "+cred+"\n")
	case "/gzip":
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = io.WriteString(zw, "you sent: "+cred+"\n")
		_ = zw.Close()
	case "/split":
		f, _ := w.(http.Flusher)
		half := len(cred) / 2
		_, _ = io.WriteString(w, "you sent: "+cred[:half])
		if f != nil {
			f.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, cred[half:]+"\n")
	case "/brotli":
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, "unreadable-body "+cred)
	default:
		http.NotFound(w, r)
	}
}

// get sends one request with one header set and reports what the upstream saw
// and what came back to the agent.
func (u *upstream) get(t *testing.T, c *http.Client, url, header, value string) result {
	t.Helper()
	u.mu.Lock()
	u.seen = ""
	u.mu.Unlock()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set(header, value)
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	u.mu.Lock()
	defer u.mu.Unlock()
	return result{status: resp.StatusCode, seen: u.seen, body: string(body), header: resp.Header.Get("X-Echo"), tls: resp.TLS}
}
