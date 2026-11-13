// Package directory is the master's record of the agents in the whole mesh, built
// from what each node reports (ADR-0017 point 6). It is never persisted: it is
// rebuilt from reports, so it cannot go stale across a restart or a promotion.
//
// An agent's name addresses one agent in the whole mesh. When two nodes report the
// same name, the first one to be known keeps it and the other is a conflict: it is
// left out of the directory (so it is not addressable through the mesh), listed so
// the operator can see it, and takes the name over if the holder goes away.
package directory

import (
	"slices"
	"sort"
	"sync"

	pb "github.com/yaad-index/personal-agent-orchestrator/internal/mesh/meshpb"
)

// Entry is one agent in the directory and the node that hosts it.
type Entry struct {
	Node  string
	Agent *pb.AgentInfo
}

// Conflict is an agent a node reported under a name another holder has.
type Conflict struct {
	Name string
	// Node is the node whose agent is left out.
	Node string
	// HeldBy is the node, or "" for the master itself, that holds the name.
	HeldBy string
}

// Directory is safe for concurrent use.
type Directory struct {
	// isLocal says whether a name belongs to an agent on the master itself, which
	// always holds it against a remote agent of the same name.
	isLocal func(name string) bool

	mu       sync.RWMutex
	reported map[string]map[string]*pb.AgentInfo // node -> name -> agent, as reported
	owner    map[string]string                   // name -> node that holds it
}

// New returns an empty directory. isLocal may be nil.
func New(isLocal func(name string) bool) *Directory {
	if isLocal == nil {
		isLocal = func(string) bool { return false }
	}
	return &Directory{isLocal: isLocal, reported: make(map[string]map[string]*pb.AgentInfo), owner: make(map[string]string)}
}

// Replace sets everything a node hosts: its full report.
func (d *Directory) Replace(node string, agents []*pb.AgentInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	old := d.reported[node]
	next := make(map[string]*pb.AgentInfo, len(agents))
	for _, a := range agents {
		if a.GetName() != "" {
			next[a.GetName()] = a
		}
	}
	d.reported[node] = next
	for name := range old {
		if _, still := next[name]; !still {
			d.releaseLocked(node, name)
		}
	}
	for name := range next {
		d.claimLocked(node, name)
	}
}

// Upsert records one agent a node reports, new or changed.
func (d *Directory) Upsert(node string, a *pb.AgentInfo) {
	if a.GetName() == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reported[node] == nil {
		d.reported[node] = make(map[string]*pb.AgentInfo)
	}
	d.reported[node][a.GetName()] = a
	d.claimLocked(node, a.GetName())
}

// Remove forgets one agent a node no longer hosts.
func (d *Directory) Remove(node, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.reported[node][name]; !ok {
		return
	}
	delete(d.reported[node], name)
	d.releaseLocked(node, name)
}

// Drop forgets everything a node hosts, when it is no longer reporting: an agent
// the master cannot hear about is not one it should route to. The node reports in
// full again when it returns.
func (d *Directory) Drop(node string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	old := d.reported[node]
	delete(d.reported, node)
	for name := range old {
		d.releaseLocked(node, name)
	}
}

// claimLocked gives node the name if nobody holds it.
func (d *Directory) claimLocked(node, name string) {
	if _, held := d.owner[name]; held || d.isLocal(name) {
		return
	}
	d.owner[name] = node
}

// releaseLocked lets go of node's hold on the name and hands it to another node
// that reported it, the lowest node id first, so the outcome does not depend on
// the order reports happened to arrive in.
func (d *Directory) releaseLocked(node, name string) {
	if d.owner[name] != node {
		return
	}
	delete(d.owner, name)
	if d.isLocal(name) {
		return
	}
	var nodes []string
	for n, m := range d.reported {
		if _, ok := m[name]; ok {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) > 0 {
		sort.Strings(nodes)
		d.owner[name] = nodes[0]
	}
}

// Lookup returns the agent a name addresses.
func (d *Directory) Lookup(name string) (Entry, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	node, ok := d.owner[name]
	if !ok || d.isLocal(name) {
		return Entry{}, false
	}
	return Entry{Node: node, Agent: d.reported[node][name]}, true
}

// Has reports whether a name is held by an agent in the directory.
func (d *Directory) Has(name string) bool {
	_, ok := d.Lookup(name)
	return ok
}

// All returns every agent in the directory, ordered by name.
func (d *Directory) All() []Entry {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Entry, 0, len(d.owner))
	for name, node := range d.owner {
		if d.isLocal(name) {
			continue
		}
		out = append(out, Entry{Node: node, Agent: d.reported[node][name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent.GetName() < out[j].Agent.GetName() })
	return out
}

// ByTag returns the agents carrying a tag, ordered by name.
func (d *Directory) ByTag(tag string) []Entry {
	var out []Entry
	for _, e := range d.All() {
		if slices.Contains(e.Agent.GetTags(), tag) {
			out = append(out, e)
		}
	}
	return out
}

// Conflicts returns the agents left out because another holds their name, ordered
// by name and then node.
func (d *Directory) Conflicts() []Conflict {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var out []Conflict
	for node, m := range d.reported {
		for name := range m {
			switch {
			case d.isLocal(name):
				out = append(out, Conflict{Name: name, Node: node})
			case d.owner[name] != node:
				out = append(out, Conflict{Name: name, Node: node, HeldBy: d.owner[name]})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Node < out[j].Node
	})
	return out
}

// Count returns how many agents a node has in the directory.
func (d *Directory) Count(node string) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	n := 0
	for name, owner := range d.owner {
		if owner == node && !d.isLocal(name) {
			n++
		}
	}
	return n
}
