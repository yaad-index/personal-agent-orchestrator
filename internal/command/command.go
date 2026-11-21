// Package command is pao's own chat commands (ADR-0026): a message whose first
// word is /pao is read here and never reaches an agent. Each command is an
// operation the node already offers its operator surface, run through the same
// checks. Help and dispatch come from one table, so help cannot list a command
// that does not exist or miss one that does.
package command

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

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
	// Agents lists the agents the operator may reach under the access matrix,
	// as the node's agent list shows them: an agent out of reach is left out, as
	// if it did not exist (ADR-0012 point 4).
	Agents(ctx context.Context) ([]Agent, error)
	// Bind sets a standing binding (ADR-0029 point 8) and returns it as set,
	// its defaults filled in.
	Bind(ctx context.Context, b Binding) (Binding, error)
	// Unbind removes a standing binding set while the node runs.
	Unbind(ctx context.Context, b Binding) (Binding, error)
	// Bindings lists the standing bindings of the agents the operator may
	// reach, of one agent when agent is not empty.
	Bindings(ctx context.Context, agent string) ([]Binding, error)
	// Spawn starts a temporary agent from recipe (ADR-0006 point 2), its stream
	// bound to channel, and returns its name. An empty name is generated from the
	// recipe. On an agent created that did not become ready it returns the name
	// with the error.
	Spawn(ctx context.Context, recipe, name, channel string) (string, error)
	// Kill harvests a temporary agent's outputs and then removes it (ADR-0006
	// point 4).
	Kill(ctx context.Context, name string) (Killed, error)
}

// Killed is what a kill reports.
type Killed struct {
	// Harvest is the id of what was harvested.
	Harvest string
	// Report is what was harvested, file by file, where the node has it; empty
	// for a kill on another node of the mesh.
	Report string
	// WorkdirKept is whether the workdir was kept.
	WorkdirKept bool
}

// Binding is one standing binding as the commands show it: a destination, by
// name, following one of an agent's streams.
type Binding struct {
	Agent string
	// Stream is "stdout" or "stderr"; empty is stdout.
	Stream string
	To     string
	// Level is a number or "log"; empty is the node's.
	Level  string
	Prefix string
	// From is where the binding was set: "config" or "runtime".
	From string
}

// Agent is one agent as /pao agents lists it.
type Agent struct {
	Name string
	Tags []string
	// State is "running" or "stopped".
	State string
}

// Caller is who sent a command, and where from.
type Caller struct {
	// Channel is the channel the command came in on. A command's answer goes
	// back there, to the caller alone (ADR-0026 point 5).
	Channel string
	// Operator is whether the caller's subject is routed as the operator.
	Operator bool
	// Later sends the caller alone a later answer, for a command whose outcome
	// comes after its reply, such as a spawn. Nil where no later answer can be
	// sent: such a command is then not available.
	Later func(ctx context.Context, text string)
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
	{word: "agents", about: "list the agents you may reach, with their tags and whether each is running", operator: true, run: runAgents},
	{word: "stream", args: "<agent> [channel] [level] | <agent> --clear", about: "send an agent's output to one channel (this one if none is named), at a level; --clear sends it to every channel again", operator: true, run: runStream},
	{word: "bind", args: "<agent> <destination> [stderr] [level|log] [prefix]", about: "make a destination (group:<name>, private:<subject>, channel:local) follow an agent's output, or with stderr its gate questions and notices", operator: true, run: runBind},
	{word: "unbind", args: "<agent> <destination> [stderr]", about: "remove a binding set with bind", operator: true, run: runUnbind},
	{word: "bindings", args: "[agent]", about: "list the standing bindings, and where each was set", operator: true, run: runBindings},
	{word: "spawn", args: "<recipe> [name]", about: "start a temporary agent from a recipe, its output on this channel; you are told when it is ready", operator: true, run: runSpawn},
	{word: "kill", args: "<agent>", about: "harvest a temporary agent's outputs, then remove it", operator: true, run: runKill},
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

func runAgents(ctx context.Context, ops Ops, _ Caller, args []string, _ string) (Result, bool) {
	if len(args) > 0 {
		return Result{}, false
	}
	agents, err := ops.Agents(ctx)
	if err != nil {
		return Result{Reply: err.Error()}, true
	}
	if len(agents) == 0 {
		return Result{Reply: "no agents you may reach"}, true
	}
	agents = slices.Clone(agents)
	slices.SortFunc(agents, func(a, b Agent) int { return strings.Compare(a.Name, b.Name) })
	var b strings.Builder
	b.WriteString("agents you may reach:")
	for _, a := range agents {
		fmt.Fprintf(&b, "\n%s: %s", a.Name, a.State)
		if len(a.Tags) > 0 {
			fmt.Fprintf(&b, "; tags %s", strings.Join(a.Tags, ", "))
		}
	}
	return Result{Reply: b.String()}, true
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

// spawnTimeout bounds a spawn run after its command was answered: provisioning
// the workdir and waiting for the agent to be ready. laterTimeout bounds sending
// its outcome, on a context of its own, so a spawn that ran out its time is still
// told. Variables for the tests.
var (
	spawnTimeout = 15 * time.Minute
	laterTimeout = 30 * time.Second
)

// runSpawn reads /pao spawn <recipe> [name]. A spawn provisions a workdir and
// waits for the agent to become ready, which can take minutes, and a channel
// handles its messages one at a time. So the command is answered at once, the
// spawn runs in the background, and its outcome is sent to the caller later.
func runSpawn(ctx context.Context, ops Ops, c Caller, args []string, _ string) (Result, bool) {
	if len(args) < 1 || len(args) > 2 {
		return Result{}, false
	}
	if c.Later == nil {
		return Result{Reply: "spawn is not available on this channel"}, true
	}
	recipe, name := args[0], ""
	if len(args) == 2 {
		name = args[1]
	}
	detached := context.WithoutCancel(ctx)
	bg, cancel := context.WithTimeout(detached, spawnTimeout)
	go func() {
		got, err := ops.Spawn(bg, recipe, name, c.Channel)
		cancel()
		tell, done := context.WithTimeout(detached, laterTimeout)
		defer done()
		c.Later(tell, spawned(recipe, got, err))
	}()
	return Result{Reply: fmt.Sprintf("spawning an agent from recipe %s; you will be told when it is ready", recipe)}, true
}

// spawned is the later answer to a spawn.
func spawned(recipe, name string, err error) string {
	switch {
	case err == nil:
		return fmt.Sprintf("%s is ready, spawned from recipe %s", name, recipe)
	case name != "":
		return fmt.Sprintf("%s was created from recipe %s but is not ready: %v\nkill it with /pao kill %s", name, recipe, err, name)
	default:
		return fmt.Sprintf("spawn from recipe %s failed: %v", recipe, err)
	}
}

func runKill(ctx context.Context, ops Ops, _ Caller, args []string, _ string) (Result, bool) {
	if len(args) != 1 {
		return Result{}, false
	}
	k, err := ops.Kill(ctx, args[0])
	if err != nil {
		return Result{Reply: err.Error()}, true
	}
	reply := fmt.Sprintf("%s is killed; its outputs are harvested as %s", args[0], k.Harvest)
	if k.Report != "" {
		reply += "\n" + k.Report
	}
	if k.WorkdirKept {
		reply += "\nthe workdir was kept"
	}
	return Result{Reply: reply}, true
}

// Streams a binding follows.
const (
	streamStdout = "stdout"
	streamStderr = "stderr"
)

// runBind reads /pao bind <agent> <destination> [stdout|stderr] [level|log]
// [prefix]. The prefix is the rest of the line, so it may hold a space.
func runBind(ctx context.Context, ops Ops, _ Caller, args []string, rest string) (Result, bool) {
	if len(args) < 2 {
		return Result{}, false
	}
	b := Binding{Agent: args[0], To: args[1]}
	_, rest = cut(rest)
	_, rest = cut(rest)
	if w, more := cut(rest); w == streamStdout || w == streamStderr {
		b.Stream, rest = w, more
	}
	if w, more := cut(rest); w == "log" || isNumber(w) {
		b.Level, rest = w, more
	}
	b.Prefix = strings.TrimSpace(rest)
	set, err := ops.Bind(ctx, b)
	if err != nil {
		return Result{Reply: err.Error()}, true
	}
	return Result{Reply: describeBinding(set, "follows")}, true
}

func runUnbind(ctx context.Context, ops Ops, _ Caller, args []string, _ string) (Result, bool) {
	if len(args) < 2 || len(args) > 3 {
		return Result{}, false
	}
	b := Binding{Agent: args[0], To: args[1]}
	if len(args) == 3 {
		if args[2] != streamStdout && args[2] != streamStderr {
			return Result{}, false
		}
		b.Stream = args[2]
	}
	gone, err := ops.Unbind(ctx, b)
	if err != nil {
		return Result{Reply: err.Error()}, true
	}
	return Result{Reply: fmt.Sprintf("%s no longer follows %s's %s", gone.To, gone.Agent, streamOf(gone))}, true
}

func runBindings(ctx context.Context, ops Ops, _ Caller, args []string, _ string) (Result, bool) {
	if len(args) > 1 {
		return Result{}, false
	}
	agent := ""
	if len(args) == 1 {
		agent = args[0]
	}
	bs, err := ops.Bindings(ctx, agent)
	if err != nil {
		return Result{Reply: err.Error()}, true
	}
	if len(bs) == 0 {
		return Result{Reply: "no standing bindings"}, true
	}
	var out strings.Builder
	out.WriteString("standing bindings:")
	for _, b := range bs {
		fmt.Fprintf(&out, "\n%s (%s)", describeBinding(b, "follows"), b.From)
	}
	return Result{Reply: out.String()}, true
}

// describeBinding says a binding in one line, by destination name.
func describeBinding(b Binding, verb string) string {
	s := fmt.Sprintf("%s %s %s's %s", b.To, verb, b.Agent, streamOf(b))
	if b.Level != "" {
		s += " at level " + b.Level
	}
	if b.Prefix != "" {
		s += fmt.Sprintf(", prefix %q", b.Prefix)
	}
	return s
}

func streamOf(b Binding) string {
	if b.Stream == "" {
		return streamStdout
	}
	return b.Stream
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// notProposable are the command words a governor may not propose (ADR-0030):
// help answers nothing to run, and a gate question is answered by the operator
// alone.
var notProposable = map[string]bool{"help": true, "allow": true, "always": true, "deny": true}

// Proposable checks that text is exactly one command a governor may propose
// (ADR-0030 point 3) and returns it trimmed, as it is shown and run: one line,
// beginning "/pao " and a command word of the table other than help and the gate's
// answers. The short form "/pao <name> <message>" is not proposed; the long form
// "/pao agent <name> <message>" says the same and cannot be read as a command.
func Proposable(text string) (string, error) {
	text = strings.TrimSpace(text)
	if strings.ContainsAny(text, "\r\n") {
		return "", fmt.Errorf("a proposal is one line")
	}
	head, rest := cut(text)
	if head != Prefix {
		return "", fmt.Errorf("a proposal begins %q", Prefix+" ")
	}
	word, _ := cut(rest)
	if _, ok := lookup(word); !ok || notProposable[word] {
		return "", fmt.Errorf("%q is not a command a governor may propose", word)
	}
	return text, nil
}
