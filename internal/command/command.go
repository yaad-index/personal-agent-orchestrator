// Package command is pao's own chat commands (ADR-0026): a message whose first
// word is /pao is read here and never reaches an agent. Each command is an
// operation the node already offers its operator surface, run through the same
// checks. Help and dispatch come from one table, so help cannot list a command
// that does not exist or miss one that does.
package command

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/core"
)

// Prefix is the first word of a command.
const Prefix = "/pao"

// Ops is what the commands do: the operator surface's own operations.
type Ops interface {
	// SetStream binds the agent's output to channel at level; an empty channel
	// clears the binding.
	SetStream(ctx context.Context, agent, channel string, level int) error
	// View pauses or resumes the agent's view.
	View(ctx context.Context, agent, action string) error
	// Decide answers a gate question: decision is "allow", "always" or "deny".
	Decide(ctx context.Context, id, decision, reason string) error
}

// Caller is who sent a command, and where from.
type Caller struct {
	// Channel is the channel the command came in on. A command's answer goes
	// back there, to the caller alone (ADR-0026 point 5).
	Channel string
	// Operator is whether the caller's subject is routed as the operator.
	Operator bool
}

// Result is what a command came to. A command that addresses an agent or a tag
// sets Address and Text: the caller routes Text to Address. Any other command sets
// Reply, the text to send the caller.
type Result struct {
	Reply   string
	Address *core.Target
	Text    string
}

// spec is one command of the table.
type spec struct {
	word  string
	args  string
	about string
	// operator marks a command only the operator may run (ADR-0026 point 3).
	operator bool
	run      func(ctx context.Context, ops Ops, c Caller, args []string, rest string) (Result, bool)
}

// table is every command, in the order help lists them.
var table = []spec{
	{word: "help", about: "show the commands you may run"}, // run by Run itself: help reads the table
	{word: "agent", args: "<name> <message>", about: "send a message to an agent (short form: /pao <name> <message>)", run: runAgent},
	{word: "tag", args: "<tag> <message>", about: "send a message to every agent with a tag", run: runTag},
	{word: "stream", args: "<agent> [channel] [level] | <agent> --clear", about: "send an agent's output to one channel (this one if none is named), at a level; --clear sends it to every channel again", operator: true, run: runStream},
	{word: "view", args: "<agent> pause|resume", about: "hold an agent's output back, or send what was held and let it through again", operator: true, run: runView},
	{word: "allow", args: "<question>", about: "allow what a gate question asks, once", operator: true, run: decide("allow")},
	{word: "always", args: "<question>", about: "allow what a gate question asks, and remember it", operator: true, run: decide("always")},
	{word: "deny", args: "<question> [reason]", about: "deny what a gate question asks", operator: true, run: decide("deny")},
}

// Words returns the command words, in table order.
func Words() []string {
	out := make([]string, len(table))
	for i, s := range table {
		out[i] = s.word
	}
	return out
}

// Is reports whether text is a command: its first word is /pao, alone or with the
// bot's own name after an "@", as a chat client writes it in a group.
func Is(text string) bool {
	head, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	head, _, _ = strings.Cut(head, "\n")
	return head == Prefix || strings.HasPrefix(head, Prefix+"@")
}

func lookup(word string) (spec, bool) {
	for _, s := range table {
		if s.word == word {
			return s, true
		}
	}
	return spec{}, false
}

// Run reads and runs one command. It never fails: what goes wrong is the reply. A
// command the caller may not run is answered as one that does not exist, so the
// answer does not reveal what exists (ADR-0026 point 3).
func Run(ctx context.Context, ops Ops, c Caller, text string) Result {
	text = strings.TrimSpace(text)
	_, rest, _ := strings.Cut(text, " ")
	word, rest := cut(rest)
	if word == "" {
		return Result{Reply: Help(c)}
	}
	s, ok := lookup(word)
	if !ok {
		// The short form: /pao <name> <message>.
		if strings.TrimSpace(rest) == "" {
			return Result{Reply: fmt.Sprintf("unknown command %q.\n%s", word, Help(c))}
		}
		return address(core.ByName, word, rest)
	}
	if s.operator && !c.Operator {
		return Result{Reply: fmt.Sprintf("unknown command %q.\n%s", word, Help(c))}
	}
	args := strings.Fields(rest)
	if s.run == nil {
		if len(args) > 0 {
			return Result{Reply: "usage: " + usage(s)}
		}
		return Result{Reply: Help(c)}
	}
	res, ok := s.run(ctx, ops, c, args, rest)
	if !ok {
		return Result{Reply: "usage: " + usage(s)}
	}
	return res
}

// cut splits off the first word of s; the rest keeps its own spacing and lines.
func cut(s string) (word, rest string) {
	s = strings.TrimLeft(s, " \t\n")
	i := strings.IndexAny(s, " \t\n")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimLeft(s[i:], " \t")
}

func usage(s spec) string {
	u := Prefix + " " + s.word
	if s.args != "" {
		u += " " + s.args
	}
	return u
}

// Help lists the commands the caller may run (ADR-0026 point 6).
func Help(c Caller) string {
	var b strings.Builder
	b.WriteString("pao commands:")
	for _, s := range table {
		if s.operator && !c.Operator {
			continue
		}
		fmt.Fprintf(&b, "\n%s: %s", usage(s), s.about)
	}
	return b.String()
}

func address(kind core.TargetKind, value, rest string) Result {
	return Result{Address: &core.Target{Kind: kind, Value: value}, Text: strings.TrimSpace(rest)}
}

func runAgent(_ context.Context, _ Ops, _ Caller, _ []string, rest string) (Result, bool) {
	return addressed(core.ByName, rest)
}

func runTag(_ context.Context, _ Ops, _ Caller, _ []string, rest string) (Result, bool) {
	return addressed(core.ByTag, rest)
}

func addressed(kind core.TargetKind, rest string) (Result, bool) {
	value, msg := cut(rest)
	if value == "" || strings.TrimSpace(msg) == "" {
		return Result{}, false
	}
	return address(kind, value, msg), true
}

func runStream(ctx context.Context, ops Ops, c Caller, args []string, _ string) (Result, bool) {
	if len(args) == 0 || len(args) > 3 {
		return Result{}, false
	}
	agent := args[0]
	if len(args) == 2 && args[1] == "--clear" {
		if err := ops.SetStream(ctx, agent, "", 0); err != nil {
			return Result{Reply: err.Error()}, true
		}
		return Result{Reply: fmt.Sprintf("%s's output goes to every channel again", agent)}, true
	}
	channel, level := c.Channel, 0
	switch len(args) {
	case 2:
		// One argument after the agent: a level if it is a number, else a channel.
		if n, err := strconv.Atoi(args[1]); err == nil {
			level = n
		} else {
			channel = args[1]
		}
	case 3:
		n, err := strconv.Atoi(args[2])
		if err != nil {
			return Result{}, false
		}
		channel, level = args[1], n
	}
	if strings.HasPrefix(channel, "-") {
		return Result{}, false
	}
	if err := ops.SetStream(ctx, agent, channel, level); err != nil {
		return Result{Reply: err.Error()}, true
	}
	at := "the node's level"
	if level > 0 {
		at = "level " + strconv.Itoa(level)
	}
	return Result{Reply: fmt.Sprintf("%s's output goes to %s only, at %s", agent, channel, at)}, true
}

func runView(ctx context.Context, ops Ops, _ Caller, args []string, _ string) (Result, bool) {
	if len(args) != 2 || (args[1] != "pause" && args[1] != "resume") {
		return Result{}, false
	}
	if err := ops.View(ctx, args[0], args[1]); err != nil {
		return Result{Reply: err.Error()}, true
	}
	if args[1] == "pause" {
		return Result{Reply: fmt.Sprintf("%s's view is paused", args[0])}, true
	}
	return Result{Reply: fmt.Sprintf("%s's view is resumed", args[0])}, true
}

func decide(decision string) func(context.Context, Ops, Caller, []string, string) (Result, bool) {
	return func(ctx context.Context, ops Ops, _ Caller, args []string, rest string) (Result, bool) {
		if len(args) == 0 || (decision != "deny" && len(args) != 1) {
			return Result{}, false
		}
		_, reason := cut(rest)
		if err := ops.Decide(ctx, args[0], decision, strings.TrimSpace(reason)); err != nil {
			return Result{Reply: err.Error()}, true
		}
		return Result{Reply: fmt.Sprintf("%s: %s", args[0], decision)}, true
	}
}
