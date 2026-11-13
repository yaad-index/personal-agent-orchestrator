package mesh

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
)

// ServerConfig is the TLS configuration for a node accepting connections from
// other nodes. TLS 1.3 only, a client certificate is required, and the client is
// accepted only if its key is on the node list and its certificate names the node
// that list gives that key.
func ServerConfig(id *Identity, peers *Peers) *tls.Config {
	return &tls.Config{
		Certificates:           []tls.Certificate{id.Certificate},
		MinVersion:             tls.VersionTLS13,
		ClientAuth:             tls.RequireAnyClientCert,
		SessionTicketsDisabled: true,
		VerifyConnection:       verifyPeer(peers, ""),
	}
}

// ClientConfig is the TLS configuration for a node dialing the node named want.
// The server is accepted only if its key is the one the list gives for want.
func ClientConfig(id *Identity, peers *Peers, want string) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.Certificate},
		MinVersion:   tls.VersionTLS13,
		// There is no certificate authority to check a chain against and no host
		// name to check: trust is the pinned key, judged in VerifyConnection, which
		// runs on every connection (VerifyPeerCertificate would not on a resumed one).
		InsecureSkipVerify:     true,
		SessionTicketsDisabled: true,
		VerifyConnection:       verifyPeer(peers, want),
	}
}

// PeerNodeID returns the id of the node on the other end of a completed
// handshake. It is the node the verified key belongs to, never anything a peer
// merely claimed in a message.
func PeerNodeID(cs tls.ConnectionState, peers *Peers) (string, bool) {
	if len(cs.PeerCertificates) == 0 {
		return "", false
	}
	entry, err := lookup(cs.PeerCertificates[0], peers)
	if err != nil {
		return "", false
	}
	return entry.ID, true
}

func verifyPeer(peers *Peers, want string) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("no certificate from the peer")
		}
		entry, err := lookup(cs.PeerCertificates[0], peers)
		if err != nil {
			return err
		}
		if want != "" && entry.ID != want {
			return fmt.Errorf("the peer is node %q, not %q", entry.ID, want)
		}
		return nil
	}
}

// lookup finds the node a peer's certificate belongs to, or says why it is not
// one: its key is not Ed25519, its key is not on the list, or the node id the
// certificate names is not the one the list gives that key.
func lookup(cert *x509.Certificate, peers *Peers) (NodeEntry, error) {
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return NodeEntry{}, errors.New("the peer's key is not an Ed25519 key")
	}
	fp, err := Fingerprint(pub)
	if err != nil {
		return NodeEntry{}, err
	}
	entry, ok := peers.ByFingerprint(fp)
	if !ok {
		return NodeEntry{}, errors.New("the peer's key is not in the mesh")
	}
	named, ok := nodeIDOf(cert)
	if !ok || named != entry.ID {
		return NodeEntry{}, fmt.Errorf("the peer's certificate does not name node %q, the node its key belongs to", entry.ID)
	}
	return entry, nil
}
