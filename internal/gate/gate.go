package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// Request is one action an agent is about to take.
type Request struct {
	Agent string
	Tool  string
	// Input is the tool's arguments as the backend gave them (JSON).
	Input json.RawMessage
}

// Verdict is a judge's answer.
type Verdict int

const (
	// Inconclusive means the judge could not decide; the operator is asked.
	Inconclusive Verdict = iota
	// VerdictAllow means the judge allows the action.
	VerdictAllow
	// VerdictDeny means the judge denies the action.
	VerdictDeny
)

// Judge decides an uncovered or fuzzy action ahead of the operator (ADR-0004
// point 5). It is an interface because a judge is an ordinary routing target
// (ADR-0003); the gate only needs its verdict.
type Judge interface {
	Judge(ctx context.Context, req Request) (Verdict, string, error)
}

// Answer is the operator's reply to a question.
type Answer struct {
	Allow bool
	// Remember asks that an approval be written back into the policy.
	Remember bool
	Reason   string
}

// Asker puts a question to the operator and waits for the answer.
type Asker interface {
	Ask(ctx context.Context, req Request, canonicalInput string) (Answer, error)
}

// Decision is the gate's final answer to the backend's hook.
type Decision struct {
	Allow  bool
	Reason string
	// Via says how it was decided: "rule", "judge", "operator" or "closed".
	Via string
}

// Gate decides actions. Judge may be nil (no judge configured): an uncovered
// action then goes straight to the operator.
type Gate struct {
	Policy *Policy
	Judge  Judge
	Asker  Asker
	// AutoAllow is the set of risk levels that need no question. Anything
	// else, including a risk level not listed, is asked.
	AutoAllow map[Risk]bool
	// AskTimeout bounds how long the operator is waited for. An unanswered
	// question is a denial: the gate fails closed.
	AskTimeout time.Duration
}

// DefaultAutoAllow is the risk levels that auto-allow when none are configured:
// reads only.
func DefaultAutoAllow() map[Risk]bool { return map[Risk]bool{RiskRead: true} }

// Decide returns allow or deny for the action. It never returns allow because
// of an error: a malformed request, a policy or judge failure, an operator who
// cannot be reached and an unanswered question are all denials.
func (g *Gate) Decide(ctx context.Context, req Request) Decision {
	canon, err := canonical(req.Input)
	if err != nil {
		return closed("the action's input could not be read: " + err.Error())
	}

	if rule, ok := g.Policy.Lookup(req.Tool, canon); ok {
		switch {
		case rule.Effect == EffectAllow:
			return Decision{Allow: true, Via: "rule", Reason: "allowed by policy"}
		case rule.Effect == EffectDeny:
			return Decision{Via: "rule", Reason: "denied by policy"}
		case g.AutoAllow[rule.Risk]:
			return Decision{Allow: true, Via: "rule", Reason: fmt.Sprintf("%s actions are allowed", rule.Risk)}
		}
		// A known but higher-risk action asks the operator directly: the policy
		// has already classified it, so there is nothing fuzzy for a judge.
		return g.askOperator(ctx, req, canon)
	}

	// Uncovered: a judge first, if there is one.
	if g.Judge != nil {
		v, reason, err := g.Judge.Judge(ctx, req)
		switch {
		case err != nil || v == Inconclusive:
			// fall through to the operator
		case v == VerdictAllow:
			return Decision{Allow: true, Via: "judge", Reason: reason}
		default:
			return Decision{Via: "judge", Reason: orDefault(reason, "denied by the judge")}
		}
	}
	return g.askOperator(ctx, req, canon)
}

func (g *Gate) askOperator(ctx context.Context, req Request, canon string) Decision {
	if g.Asker == nil {
		return closed("no operator is configured to approve this action")
	}
	timeout := g.AskTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ans, err := g.Asker.Ask(ctx, req, canon)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return closed("no operator answer arrived in time")
		}
		return closed("the operator could not be asked: " + err.Error())
	}
	if !ans.Allow {
		return Decision{Via: "operator", Reason: orDefault(ans.Reason, "denied by the operator")}
	}
	if ans.Remember {
		if err := g.Policy.Remember(req.Tool, canon); err != nil {
			// The action is approved for this once; only the write-back failed.
			return Decision{Allow: true, Via: "operator", Reason: "approved by the operator (not remembered: " + err.Error() + ")"}
		}
	}
	return Decision{Allow: true, Via: "operator", Reason: "approved by the operator"}
}

func closed(reason string) Decision {
	return Decision{Via: "closed", Reason: reason}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// canonical re-encodes JSON with sorted keys and no insignificant whitespace,
// so a rule matches the same action however the backend ordered its arguments.
func canonical(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "{}", nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep numbers exactly as written; float64 would reshape them
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	if dec.More() {
		return "", errors.New("trailing data after the input")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
