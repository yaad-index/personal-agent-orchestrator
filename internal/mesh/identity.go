package mesh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// nodeURIScheme and nodeURIHost make the URI a certificate carries to say which
// node it was made for: pao-node://<id>.
const nodeURIScheme = "pao-node"

// ErrKeyExists is returned by Generate when a key is already there. A node's key
// is its identity in every other node's list, so it is never replaced by accident.
var ErrKeyExists = errors.New("a key already exists")

// Identity is a node's key and the certificate made from it.
type Identity struct {
	// NodeID is the node's id in the mesh document.
	NodeID string
	// Fingerprint is the fingerprint of the node's public key.
	Fingerprint string
	// Certificate is what the node presents in a handshake.
	Certificate tls.Certificate
}

// Fingerprint returns "sha256:" and the hex SHA-256 of the key's PKIX encoding.
func Fingerprint(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Generate creates a new key for the node, writes it to path with permissions
// that let only its owner read it, and returns the identity. It refuses to
// overwrite a key that exists.
func Generate(path, nodeID string) (*Identity, error) {
	if !idPattern.MatchString(nodeID) {
		return nil, fmt.Errorf("node id %q must be lowercase letters, digits, '-' or '_'", nodeID)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := writeKeyOnce(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		return nil, err
	}
	return identityFrom(nodeID, priv, pub)
}

// writeKeyOnce writes the key so that the file at path either does not exist or is
// complete, and never replaces one that exists. It writes a private temporary file
// beside it and links that into place: a link fails if the name is taken, and a
// process that dies part way leaves a temporary file, not a half-written key that
// would block the next attempt.
func writeKeyOnce(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".key-*") // created 0600, exclusively
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, werr := tmp.Write(content)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w at %s", ErrKeyExists, path)
		}
		return err
	}
	return nil
}

// Load reads the node's key from path. It refuses a key file that anyone but its
// owner can read: a key other users or agents could copy is not an identity.
func Load(path, nodeID string) (*Identity, error) {
	if !idPattern.MatchString(nodeID) {
		return nil, fmt.Errorf("node id %q must be lowercase letters, digits, '-' or '_'", nodeID)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by others (mode %o); the key must be private to its owner", path, info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("%s: not a private key", path)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: the key is not an Ed25519 key", path)
	}
	return identityFrom(nodeID, priv, priv.Public().(ed25519.PublicKey))
}

func identityFrom(nodeID string, priv ed25519.PrivateKey, pub ed25519.PublicKey) (*Identity, error) {
	fp, err := Fingerprint(pub)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID},
		URIs:         []*url.URL{{Scheme: nodeURIScheme, Host: nodeID}},
		// The certificate's dates are not part of the trust: a peer is trusted for
		// the key it holds, which the node list pins. They are wide so a clock that
		// is off does not matter.
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(100 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, err
	}
	return &Identity{
		NodeID:      nodeID,
		Fingerprint: fp,
		Certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv},
	}, nil
}

// nodeIDOf returns the node id a certificate was made for.
func nodeIDOf(cert *x509.Certificate) (string, bool) {
	if len(cert.URIs) != 1 || cert.URIs[0].Scheme != nodeURIScheme {
		return "", false
	}
	u := cert.URIs[0]
	// Exactly pao-node://<id>: no user, port, path, query or fragment to read as
	// something else.
	if u.User != nil || u.Port() != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	id := u.Hostname()
	if !idPattern.MatchString(id) {
		return "", false
	}
	return id, true
}
