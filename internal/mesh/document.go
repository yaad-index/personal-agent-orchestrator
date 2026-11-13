// Package mesh holds the identity and trust of a pao node in a multi-node mesh
// (ADR-0013, ADR-0017): the mesh document that lists the nodes, each node's key,
// and the TLS configuration that lets two nodes talk only if each is on the
// other's list. It carries no messages: the transport that does is built on it.
package mesh

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	idPattern          = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// NodeEntry is one node in the mesh.
type NodeEntry struct {
	// ID names the node. It is unique in the mesh.
	ID string `yaml:"id"`
	// Addr is the host:port other nodes dial to reach this one.
	Addr string `yaml:"addr"`
	// Fingerprint is the fingerprint of the node's public key, as `pao mesh keygen`
	// prints it: "sha256:" and 64 hex digits. It is the trust anchor: a peer is
	// whoever holds the key with this fingerprint.
	Fingerprint string `yaml:"fingerprint"`
}

// Document is the mesh document: what every node in the mesh holds the same
// copy of (ADR-0017 point 9). It never contains a secret.
type Document struct {
	Nodes []NodeEntry `yaml:"nodes"`
}

// LoadDocument reads and validates the mesh document at path. Unknown keys are
// an error, so a typo is not silently ignored.
func LoadDocument(path string) (*Document, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d Document
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%s: %s", path, strings.TrimSpace(err.Error()))
	}
	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &d, nil
}

// Validate checks that the node list can serve as a trust anchor: ids are well
// formed and unique, addresses are host:port, fingerprints are well formed and
// unique (two ids with one key could not be told apart).
func (d *Document) Validate() error {
	if len(d.Nodes) == 0 {
		return fmt.Errorf("nodes: the mesh lists no nodes")
	}
	ids := make(map[string]bool)
	fps := make(map[string]string)
	for i, n := range d.Nodes {
		where := fmt.Sprintf("nodes[%d]", i)
		if !idPattern.MatchString(n.ID) {
			return fmt.Errorf("%s: id %q must be lowercase letters, digits, '-' or '_'", where, n.ID)
		}
		if ids[n.ID] {
			return fmt.Errorf("%s: node id %q is listed twice", where, n.ID)
		}
		ids[n.ID] = true
		host, port, err := net.SplitHostPort(n.Addr)
		if err != nil || host == "" || port == "" {
			return fmt.Errorf("%s (%s): addr %q must be host:port", where, n.ID, n.Addr)
		}
		if !fingerprintPattern.MatchString(n.Fingerprint) {
			return fmt.Errorf("%s (%s): fingerprint must be sha256: and 64 lowercase hex digits", where, n.ID)
		}
		if other, dup := fps[n.Fingerprint]; dup {
			return fmt.Errorf("%s (%s): the same key is listed for %q", where, n.ID, other)
		}
		fps[n.Fingerprint] = n.ID
	}
	return nil
}

// Entry returns the entry for a node id.
func (d *Document) Entry(id string) (NodeEntry, bool) {
	for _, n := range d.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return NodeEntry{}, false
}

// Peers returns the document as a lookup by fingerprint, which is how a
// connection is judged.
func (d *Document) Peers() *Peers {
	p := &Peers{byFingerprint: make(map[string]NodeEntry, len(d.Nodes))}
	for _, n := range d.Nodes {
		p.byFingerprint[n.Fingerprint] = n
	}
	return p
}

// Peers is the set of nodes a connection may be with.
type Peers struct {
	byFingerprint map[string]NodeEntry
}

// ByFingerprint returns the node whose key has the fingerprint.
func (p *Peers) ByFingerprint(fp string) (NodeEntry, bool) {
	n, ok := p.byFingerprint[fp]
	return n, ok
}
