package confine

import (
	"fmt"
	"strings"

	"github.com/yaad-index/personal-agent-orchestrator/internal/shell"
)

// BridgeAddr is where, inside, the command reaches the proxy.
const BridgeAddr = "127.0.0.1:3128"

// Spec is what a confined command runs under.
type Spec struct {
	// Socket is the proxy's socket on the host; the bridge connects to it.
	Socket string `json:"socket"`
	// Mask lists host paths the command does not see: a directory shows
	// empty, a file shows as empty, and a path that does not exist stays
	// unseen even if the host makes it while the command runs.
	Mask []string `json:"mask"`
	// Keep lists paths that stay visible even inside a masked directory, such
	// as the sockets of pao's own surfaces and the agent's own working
	// directory. A missing path is left alone.
	Keep []string `json:"keep,omitempty"`
	// Forwards are further loopback addresses, inside, carried to sockets on
	// the host: pao's own surfaces an agent reaches over TCP.
	Forwards []Forward `json:"forwards,omitempty"`
	// Command is the program and its arguments.
	Command []string `json:"command"`
}

// Forward carries a loopback address inside to a socket on the host.
type Forward struct {
	Addr   string `json:"addr"`
	Socket string `json:"socket"`
}

// PaneCommand is the shell command that runs inner, a shell command line,
// confined under s through self's egress-exec command. s.Command is ignored:
// inner is run by sh.
func PaneCommand(self string, s Spec, inner string) string {
	var b strings.Builder
	b.WriteString("exec " + shell.Quote(self) + " egress-exec --socket " + shell.Quote(s.Socket))
	for _, m := range s.Mask {
		b.WriteString(" --mask " + shell.Quote(m))
	}
	for _, k := range s.Keep {
		b.WriteString(" --keep " + shell.Quote(k))
	}
	for _, f := range s.Forwards {
		b.WriteString(" --forward " + shell.Quote(f.Addr+"="+f.Socket))
	}
	b.WriteString(" -- sh -c " + shell.Quote(inner))
	return b.String()
}

// ParseForward reads a forward written addr=socket.
func ParseForward(v string) (Forward, error) {
	addr, sock, ok := strings.Cut(v, "=")
	if !ok || addr == "" || sock == "" {
		return Forward{}, fmt.Errorf("forward %q is not addr=socket", v)
	}
	return Forward{Addr: addr, Socket: sock}, nil
}
