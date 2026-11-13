// Package gate is the approval gate (ADR-0004): every agent action is decided
// at the tool-call boundary, before it runs, as allow or deny. An action is
// mapped to a risk level by a policy of rules; a low-risk one auto-allows, a
// higher-risk or uncovered one is escalated (a judge first, the operator
// last), and the gate fails closed throughout. What the operator approves can
// be written back into the policy so the same request is covered next time.
package gate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// Risk is how consequential an action is (ADR-0004 point 2).
type Risk string

const (
	RiskRead        Risk = "read"
	RiskPrivate     Risk = "private"
	RiskShared      Risk = "shared"
	RiskPublic      Risk = "public"
	RiskDestructive Risk = "destructive"
)

// Valid reports whether r is a known risk level.
func (r Risk) Valid() bool {
	switch r {
	case RiskRead, RiskPrivate, RiskShared, RiskPublic, RiskDestructive:
		return true
	}
	return false
}

// Effect is an explicit decision a rule can force, whatever the risk.
type Effect string

const (
	// EffectNone leaves the decision to the rule's risk level.
	EffectNone Effect = ""
	// EffectAllow always allows a matching action.
	EffectAllow Effect = "allow"
	// EffectDeny always denies a matching action.
	EffectDeny Effect = "deny"
)

// Rule maps an action to a risk level or forces a decision. Rules are checked
// in order and the first match wins, so a deny placed before a broader allow
// holds.
type Rule struct {
	// Tool is the tool's name; it must match exactly.
	Tool string `json:"tool"`
	// Match is a regular expression over the action's canonical input (the
	// tool's arguments as JSON with sorted keys). Empty matches every input.
	Match string `json:"match,omitempty"`
	// Risk is the risk level the action maps to.
	Risk Risk `json:"risk,omitempty"`
	// Effect forces a decision regardless of risk.
	Effect Effect `json:"effect,omitempty"`
	// Source records where the rule came from: "builtin", "operator" (written
	// back from an approval) or "config".
	Source string `json:"source,omitempty"`

	re *regexp.Regexp
}

func (r *Rule) compile() error {
	if r.Tool == "" {
		return errors.New("rule has no tool")
	}
	if r.Risk == "" && r.Effect == EffectNone {
		return fmt.Errorf("rule for %q has neither a risk nor an effect", r.Tool)
	}
	if r.Risk != "" && !r.Risk.Valid() {
		return fmt.Errorf("rule for %q: unknown risk %q", r.Tool, r.Risk)
	}
	if r.Effect != EffectNone && r.Effect != EffectAllow && r.Effect != EffectDeny {
		return fmt.Errorf("rule for %q: unknown effect %q", r.Tool, r.Effect)
	}
	if r.Match != "" {
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return fmt.Errorf("rule for %q: match: %w", r.Tool, err)
		}
		r.re = re
	}
	return nil
}

func (r *Rule) matches(tool, input string) bool {
	if r.Tool != tool {
		return false
	}
	return r.re == nil || r.re.MatchString(input)
}

// Policy is the ordered rule set. It is accruing data, not a table baked into
// the code (ADR-0004 point 6): it is loaded from a file and grows as the
// operator approves actions. Safe for concurrent use.
type Policy struct {
	mu    sync.RWMutex
	path  string
	rules []Rule
}

// Builtin returns the rules every policy starts with: tools that only read.
func Builtin() []Rule {
	var rs []Rule
	for _, t := range []string{"Read", "Glob", "Grep", "LS", "TodoWrite"} {
		rs = append(rs, Rule{Tool: t, Risk: RiskRead, Source: "builtin"})
	}
	return rs
}

// LoadPolicy reads the policy file at path (a missing file is an empty
// policy) and puts the builtin rules after whatever it holds, so a stored
// deny can precede a builtin allow.
func LoadPolicy(path string) (*Policy, error) {
	p := &Policy{path: path}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		dec := json.NewDecoder(bytesReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p.rules); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	p.rules = append(p.rules, Builtin()...)
	for i := range p.rules {
		if err := p.rules[i].compile(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return p, nil
}

// Lookup returns the first rule matching the action.
func (p *Policy) Lookup(tool, canonicalInput string) (Rule, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.rules {
		if p.rules[i].matches(tool, canonicalInput) {
			return p.rules[i], true
		}
	}
	return Rule{}, false
}

// Remember writes back an operator approval: a rule that allows exactly this
// tool with exactly this input. It goes before the builtin rules and is
// persisted before it takes effect, so an approval is never remembered only in
// memory.
func (p *Policy) Remember(tool, canonicalInput string) error {
	r := Rule{Tool: tool, Match: "^" + regexp.QuoteMeta(canonicalInput) + "$", Effect: EffectAllow, Source: "operator"}
	if err := r.compile(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	stored := p.storedLocked()
	stored = append(stored, r)
	if err := p.persist(stored); err != nil {
		return err
	}
	// Insert ahead of the builtin tail.
	n := len(p.rules) - len(Builtin())
	p.rules = append(p.rules[:n:n], append([]Rule{r}, p.rules[n:]...)...)
	return nil
}

// storedLocked returns the rules that belong in the file: everything but the
// builtin tail.
func (p *Policy) storedLocked() []Rule {
	n := len(p.rules) - len(Builtin())
	out := make([]Rule, n)
	copy(out, p.rules[:n])
	return out
}

func (p *Policy) persist(rules []Rule) error {
	if p.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.path), ".policy-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p.path)
}
