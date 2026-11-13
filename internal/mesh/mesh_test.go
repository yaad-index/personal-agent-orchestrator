package mesh

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- identity ---

func keyPath(t *testing.T) string { return filepath.Join(t.TempDir(), "state", "mesh.key") }

func TestGenerateWritesAPrivateKeyAndReturnsItsIdentity(t *testing.T) {
	path := keyPath(t)
	id, err := Generate(path, "n1")
	require.NoError(t, err)

	assert.Equal(t, "n1", id.NodeID)
	assert.Regexp(t, regexp.MustCompile(`^sha256:[0-9a-f]{64}$`), id.Fingerprint)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Zero(t, dirInfo.Mode().Perm()&0o077, "the directory it is created in is private too")
}

func TestLoadGivesBackTheSameIdentity(t *testing.T) {
	path := keyPath(t)
	made, err := Generate(path, "n1")
	require.NoError(t, err)
	loaded, err := Load(path, "n1")
	require.NoError(t, err)
	assert.Equal(t, made.Fingerprint, loaded.Fingerprint)
	assert.Equal(t, "n1", loaded.NodeID)
}

func TestTwoNodesGetDifferentKeys(t *testing.T) {
	a, err := Generate(keyPath(t), "a")
	require.NoError(t, err)
	b, err := Generate(keyPath(t), "b")
	require.NoError(t, err)
	assert.NotEqual(t, a.Fingerprint, b.Fingerprint)
}

func TestGenerateNeverReplacesAKey(t *testing.T) {
	path := keyPath(t)
	first, err := Generate(path, "n1")
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	_, err = Generate(path, "n1")
	require.ErrorIs(t, err, ErrKeyExists)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the identity every other node lists is untouched")
	loaded, err := Load(path, "n1")
	require.NoError(t, err)
	assert.Equal(t, first.Fingerprint, loaded.Fingerprint)
}

func TestGenerateLeavesOnlyTheKeyBehind(t *testing.T) {
	path := keyPath(t)
	_, err := Generate(path, "n1")
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary file is left beside the key")
	assert.Equal(t, "mesh.key", entries[0].Name())

	_, err = Generate(path, "n1")
	require.ErrorIs(t, err, ErrKeyExists)
	entries, err = os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "a refused generate leaves nothing behind either")
}

func TestTheKeyFileIsNeverSeenHalfWritten(t *testing.T) {
	// A key that appears at its final name appears whole: a reader racing the
	// generator either finds no file or a loadable one.
	path := keyPath(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Generate(path, "n1")
	}()
	for {
		select {
		case <-done:
			_, err := Load(path, "n1")
			require.NoError(t, err)
			return
		default:
			if _, err := os.Stat(path); err == nil {
				_, lerr := Load(path, "n1")
				require.NoError(t, lerr, "a file at the final name is a complete key")
			}
		}
	}
}

func TestLoadRefusesAKeyOthersCanRead(t *testing.T) {
	path := keyPath(t)
	_, err := Generate(path, "n1")
	require.NoError(t, err)
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o666} {
		require.NoError(t, os.Chmod(path, mode))
		_, err := Load(path, "n1")
		require.Error(t, err, "mode %o", mode)
		assert.Contains(t, err.Error(), "readable by others")
	}
	require.NoError(t, os.Chmod(path, 0o400))
	_, err = Load(path, "n1")
	assert.NoError(t, err, "read-only for the owner is fine")
}

func TestLoadRefusesWhatIsNotAnEd25519Key(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, body []byte) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, body, 0o600))
		return p
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(ec)
	require.NoError(t, err)

	_, err = Load(write("ec.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "n1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an Ed25519")
	_, err = Load(write("junk.key", []byte("not a key")), "n1")
	require.Error(t, err)
	_, err = Load(write("cert.key", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "n1")
	require.Error(t, err)
	_, err = Load(filepath.Join(dir, "missing.key"), "n1")
	require.Error(t, err)
}

func TestNodeIDsMustBeWellFormed(t *testing.T) {
	for _, id := range []string{"", "A", "a b", "a/b", "../x", "a:1", "a@b"} {
		_, err := Generate(keyPath(t), id)
		assert.Error(t, err, "generate %q", id)
		path := keyPath(t)
		_, gerr := Generate(path, "ok")
		require.NoError(t, gerr)
		_, err = Load(path, id)
		assert.Error(t, err, "load %q", id)
	}
}

func TestTheCertificateNamesTheNodeAndCarriesTheKey(t *testing.T) {
	id, err := Generate(keyPath(t), "n1")
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(id.Certificate.Certificate[0])
	require.NoError(t, err)
	name, ok := nodeIDOf(cert)
	require.True(t, ok)
	assert.Equal(t, "n1", name)
	fp, err := Fingerprint(cert.PublicKey.(ed25519.PublicKey))
	require.NoError(t, err)
	assert.Equal(t, id.Fingerprint, fp)
}

func TestNodeIDOfRejectsCertificatesThatDoNotNameExactlyOneNode(t *testing.T) {
	assert.False(t, okID(&x509.Certificate{}))
	assert.False(t, okID(&x509.Certificate{URIs: uris("https://n1")}), "another scheme")
	assert.False(t, okID(&x509.Certificate{URIs: uris("pao-node://n1", "pao-node://n2")}), "two nodes")
	assert.False(t, okID(&x509.Certificate{URIs: uris("pao-node://n1@evil")}))
	assert.False(t, okID(&x509.Certificate{URIs: uris("pao-node:///x")}))
	assert.False(t, okID(&x509.Certificate{URIs: uris("pao-node://n1:80")}), "a port")
	assert.False(t, okID(&x509.Certificate{URIs: uris("pao-node://n1/path")}), "a path")
	assert.False(t, okID(&x509.Certificate{URIs: uris("pao-node://n1?x=1")}), "a query")
	assert.False(t, okID(&x509.Certificate{URIs: uris("pao-node://N1")}), "not a well-formed id")
	assert.True(t, okID(&x509.Certificate{URIs: uris("pao-node://n1")}))
}

func okID(c *x509.Certificate) bool {
	_, ok := nodeIDOf(c)
	return ok
}

func uris(ss ...string) []*urlT {
	var out []*urlT
	for _, s := range ss {
		u, err := parseURL(s)
		if err != nil {
			panic(err)
		}
		out = append(out, u)
	}
	return out
}

// --- document ---

const fp1 = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
const fp2 = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

func writeDoc(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mesh.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

func TestLoadDocument(t *testing.T) {
	d, err := LoadDocument(writeDoc(t, "nodes:\n  - id: n1\n    addr: 10.0.0.1:7745\n    fingerprint: "+fp1+"\n  - id: n2\n    addr: host.example:7745\n    fingerprint: "+fp2+"\n"))
	require.NoError(t, err)
	require.Len(t, d.Nodes, 2)
	e, ok := d.Entry("n2")
	require.True(t, ok)
	assert.Equal(t, "host.example:7745", e.Addr)
	_, ok = d.Entry("n3")
	assert.False(t, ok)
	n, ok := d.Peers().ByFingerprint(fp1)
	require.True(t, ok)
	assert.Equal(t, "n1", n.ID)
	_, ok = d.Peers().ByFingerprint("sha256:" + strings.Repeat("0", 64))
	assert.False(t, ok)
}

func TestDocumentValidation(t *testing.T) {
	node := func(id, addr, fp string) string {
		return "  - id: " + id + "\n    addr: " + addr + "\n    fingerprint: " + fp + "\n"
	}
	cases := map[string]string{
		"empty":                 "nodes: []\n",
		"no nodes key":          "{}\n",
		"unknown key":           "nodes:\n" + node("a", "h:1", fp1) + "extra: 1\n",
		"unknown field":         "nodes:\n" + node("a", "h:1", fp1) + "    note: x\n",
		"bad id":                "nodes:\n" + node("A", "h:1", fp1),
		"duplicate id":          "nodes:\n" + node("a", "h:1", fp1) + node("a", "h:2", fp2),
		"no port":               "nodes:\n" + node("a", "h", fp1),
		"no host":               "nodes:\n" + node("a", ":1", fp1),
		"bad fingerprint":       "nodes:\n" + node("a", "h:1", "abc"),
		"uppercase fingerprint": "nodes:\n" + node("a", "h:1", "sha256:"+strings.Repeat("A", 64)),
		"wrong algorithm":       "nodes:\n" + node("a", "h:1", "sha1:"+strings.Repeat("1", 64)),
		"short fingerprint":     "nodes:\n" + node("a", "h:1", "sha256:11"),
		"one key for two ids":   "nodes:\n" + node("a", "h:1", fp1) + node("b", "h:2", fp1),
	}
	for name, body := range cases {
		_, err := LoadDocument(writeDoc(t, body))
		assert.Error(t, err, name)
	}
	_, err := LoadDocument(filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Error(t, err)
}

// --- the handshake ---

// mesh makes n identities and the peers lookup that lists all of them.
func newMesh(t *testing.T, names ...string) (map[string]*Identity, *Peers) {
	t.Helper()
	ids := make(map[string]*Identity)
	d := &Document{}
	for i, n := range names {
		id, err := Generate(keyPath(t), n)
		require.NoError(t, err)
		ids[n] = id
		d.Nodes = append(d.Nodes, NodeEntry{ID: n, Addr: "127.0.0.1:" + string(rune('1'+i)), Fingerprint: id.Fingerprint})
	}
	require.NoError(t, d.Validate())
	return ids, d.Peers()
}

// handshake runs a TLS handshake between a server and a client configuration
// over loopback and returns each side's error, and the server's view.
func handshake(t *testing.T, server, client *tls.Config) (serverState, clientState tls.ConnectionState, serverErr, clientErr error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := ln.Accept()
		if err != nil {
			serverErr = err
			return
		}
		defer func() { _ = raw.Close() }()
		_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
		conn := tls.Server(raw, server)
		serverErr = conn.Handshake()
		serverState = conn.ConnectionState()
		if serverErr == nil {
			// In TLS 1.3 the client finishes its handshake before the server has
			// judged its certificate; the server's refusal reaches the client on
			// its next read, so the server reads to surface it either way.
			buf := make([]byte, 1)
			_, _ = conn.Read(buf)
		}
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	conn := tls.Client(raw, client)
	clientErr = conn.Handshake()
	var readErr error
	if clientErr == nil {
		// Send a byte and read: a server that rejected the client closes with an
		// alert, which the read reports.
		_, _ = conn.Write([]byte("x"))
		buf := make([]byte, 1)
		_, readErr = conn.Read(buf)
		_ = conn.CloseWrite()
	}
	clientState = conn.ConnectionState()
	<-done
	if clientErr == nil && serverErr != nil {
		clientErr = readErr
	}
	return
}

func TestNodesOnEachOthersListsTalkAndEachKnowsWhoItIsTalkingTo(t *testing.T) {
	ids, peers := newMesh(t, "a", "b")
	sState, cState, sErr, cErr := handshake(t, ServerConfig(ids["b"], peers), ClientConfig(ids["a"], peers, "b"))
	require.NoError(t, sErr)
	require.NoError(t, cErr)

	who, ok := PeerNodeID(sState, peers)
	require.True(t, ok)
	assert.Equal(t, "a", who, "the server knows the caller by its verified key")
	who, ok = PeerNodeID(cState, peers)
	require.True(t, ok)
	assert.Equal(t, "b", who)
	assert.Equal(t, uint16(tls.VersionTLS13), sState.Version)
}

func TestAClientWhoseKeyIsNotListedIsRefused(t *testing.T) {
	ids, peers := newMesh(t, "a", "b")
	strangerIDs, _ := newMesh(t, "stranger")
	stranger := strangerIDs["stranger"]

	// The stranger trusts the real mesh (so it would talk), but the mesh does not
	// list the stranger.
	_, _, sErr, cErr := handshake(t, ServerConfig(ids["b"], peers), ClientConfig(stranger, peers, "b"))
	require.Error(t, sErr, "the server refuses a key that is not on its list")
	assert.Error(t, cErr, "and the client finds out")
}

func TestAServerWhoseKeyIsNotListedIsRefused(t *testing.T) {
	ids, peers := newMesh(t, "a", "b")
	strangerIDs, _ := newMesh(t, "stranger")
	stranger := strangerIDs["stranger"]

	_, _, _, cErr := handshake(t, ServerConfig(stranger, peers), ClientConfig(ids["a"], peers, "b"))
	require.Error(t, cErr, "a client does not talk to a server that is not any node it trusts")
}

func TestAClientDialingOneNodeRefusesAnotherListedNode(t *testing.T) {
	ids, peers := newMesh(t, "a", "b", "c")
	_, _, _, cErr := handshake(t, ServerConfig(ids["c"], peers), ClientConfig(ids["a"], peers, "b"))
	require.Error(t, cErr)
	assert.Contains(t, cErr.Error(), `not "b"`, "node c answered where node b was dialed: a listed key is not the key of the node meant")
}

func TestAKeyOnTheListIsNotEnoughWithACertificateNamingAnotherNode(t *testing.T) {
	ids, peers := newMesh(t, "a", "b")
	// A certificate made from a's key but naming b: the key is listed, as a, and
	// the certificate claims to be b.
	forged := forgeCertNaming(t, ids["a"], "b")

	_, _, sErr, _ := handshake(t, ServerConfig(ids["b"], peers), ClientConfig(forged, peers, "b"))
	require.Error(t, sErr)
	assert.Contains(t, sErr.Error(), "does not name node")
}

func TestOnlyTLS13Talks(t *testing.T) {
	ids, peers := newMesh(t, "a", "b")
	// A client that would speak TLS 1.2 is refused by the server's own floor, not
	// merely by the client's: the client's floor is lowered to let it try.
	client := ClientConfig(ids["a"], peers, "b")
	client.MinVersion, client.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	_, _, sErr, cErr := handshake(t, ServerConfig(ids["b"], peers), client)
	require.Error(t, sErr)
	assert.Contains(t, sErr.Error(), "unsupported versions")
	assert.Error(t, cErr)

	// And a server that would speak 1.2 is refused by the client's floor.
	server := ServerConfig(ids["b"], peers)
	server.MinVersion, server.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	_, _, _, cErr = handshake(t, server, ClientConfig(ids["a"], peers, "b"))
	assert.Error(t, cErr)
}

func TestAClientWithoutACertificateIsRefused(t *testing.T) {
	ids, peers := newMesh(t, "a", "b")
	client := ClientConfig(ids["a"], peers, "b")
	client.Certificates = nil
	_, _, sErr, _ := handshake(t, ServerConfig(ids["b"], peers), client)
	assert.Error(t, sErr, "every connection is mutual: the server does not talk to a caller that is no one")
}

func TestAnRSAKeyIsNotAMeshKey(t *testing.T) {
	ids, peers := newMesh(t, "a", "b")
	rogue := rsaIdentity(t, "a")
	_, _, sErr, _ := handshake(t, ServerConfig(ids["b"], peers), ClientConfig(rogue, peers, "b"))
	require.Error(t, sErr)
	assert.Contains(t, sErr.Error(), "not an Ed25519")
}

func TestSessionsAreNeverResumed(t *testing.T) {
	ids, peers := newMesh(t, "a", "b")
	assert.True(t, ServerConfig(ids["b"], peers).SessionTicketsDisabled, "a resumed connection would skip the certificate check this relies on")
	assert.True(t, ClientConfig(ids["a"], peers, "b").SessionTicketsDisabled)
	assert.Equal(t, uint16(tls.VersionTLS13), ServerConfig(ids["b"], peers).MinVersion)
	assert.Equal(t, tls.RequireAnyClientCert, ServerConfig(ids["b"], peers).ClientAuth)
}

func TestPeerNodeIDOfAnUnlistedOrAbsentPeerIsNobody(t *testing.T) {
	_, peers := newMesh(t, "a")
	_, ok := PeerNodeID(tls.ConnectionState{}, peers)
	assert.False(t, ok)

	stranger := rsaIdentity(t, "a")
	cert, err := x509.ParseCertificate(stranger.Certificate.Certificate[0])
	require.NoError(t, err)
	_, ok = PeerNodeID(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}, peers)
	assert.False(t, ok)
}

// --- helpers ---

func forgeCertNaming(t *testing.T, id *Identity, name string) *Identity {
	t.Helper()
	priv := id.Certificate.PrivateKey.(ed25519.PrivateKey)
	forged, err := identityFrom(name, priv, priv.Public().(ed25519.PublicKey))
	require.NoError(t, err)
	return forged
}

func rsaIdentity(t *testing.T, name string) *Identity {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		URIs:      []*urlT{{Scheme: "pao-node", Host: name}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	require.NoError(t, err)
	return &Identity{NodeID: name, Certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}}
}
