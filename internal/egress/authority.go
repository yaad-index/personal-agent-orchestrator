package egress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"sync"
	"time"
)

// authority is one run's certificate authority. It exists only in memory, and
// it is name-constrained to the run's inject hosts, so a trust file that
// reaches further than the agent still vouches for nothing else.
type authority struct {
	cert  *x509.Certificate
	key   *ecdsa.PrivateKey
	pem   []byte
	mu    sync.Mutex
	leafs map[string]*tls.Certificate
}

const certLife = 30 * 24 * time.Hour

func newAuthority(agent string, hosts []string) (*authority, error) {
	// An empty name constraint is no constraint at all.
	if len(hosts) == 0 {
		return nil, errors.New("an authority needs at least one host")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:                serial,
		Subject:                     pkix.Name{CommonName: "pao egress " + agent},
		NotBefore:                   now.Add(-time.Minute),
		NotAfter:                    now.Add(certLife),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         hosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &authority{
		cert:  cert,
		key:   key,
		pem:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		leafs: map[string]*tls.Certificate{},
	}, nil
}

// leaf returns the certificate the proxy shows the agent for host.
func (a *authority) leaf(host string) (*tls.Certificate, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.leafs[host]; ok {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    a.cert.NotBefore,
		NotAfter:     a.cert.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	a.leafs[host] = c
	return c, nil
}

func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}
