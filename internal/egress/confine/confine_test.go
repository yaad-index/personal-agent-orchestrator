//go:build linux

package confine

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// actEnv names what the test binary, run as the confined command, checks from
// inside. It exits 0 when the check holds and says why on stderr when not.
const actEnv = "PAO_CONFINE_TEST_ACT"

func TestMain(m *testing.M) {
	if IsInner() {
		os.Exit(Inner())
	}
	if act := os.Getenv(actEnv); act != "" {
		os.Exit(inside(act))
	}
	os.Exit(m.Run())
}

func inside(act string) int {
	check := func(ok bool, format string, a ...any) int {
		if ok {
			return 0
		}
		_, _ = fmt.Fprintf(os.Stderr, format+"\n", a...)
		return 1
	}
	switch act {
	case "no-route":
		c, err := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second)
		if c != nil {
			_ = c.Close()
		}
		if err == nil {
			return check(false, "reached the outside directly")
		}
		_, err = net.LookupHost("example.com")
		return check(err != nil, "resolved a name")
	case "bridge":
		c, err := net.DialTimeout("tcp", BridgeAddr, 2*time.Second)
		if err != nil {
			return check(false, "dial bridge: %v", err)
		}
		defer func() { _ = c.Close() }()
		_, _ = fmt.Fprintln(c, "ping")
		line, err := bufio.NewReader(c).ReadString('\n')
		return check(err == nil && line == "pong\n", "bridge answered %q, %v", line, err)
	case "forward":
		c, err := net.DialTimeout("tcp", os.Getenv("FORWARD_ADDR"), 2*time.Second)
		if err != nil {
			return check(false, "dial forward: %v", err)
		}
		defer func() { _ = c.Close() }()
		_, _ = fmt.Fprintln(c, "ping")
		line, err := bufio.NewReader(c).ReadString('\n')
		return check(err == nil && line == "pong\n", "forward answered %q, %v", line, err)
	case "kept":
		if _, err := os.Stat(os.Getenv("HIDDEN_FILE")); err == nil {
			return check(false, "a file in the masked dir that is not kept is visible")
		}
		b, err := os.ReadFile(os.Getenv("KEPT_FILE"))
		if err != nil || string(b) != "mine" {
			return check(false, "a kept dir's file reads %q, %v", b, err)
		}
		return inside("forward")
	case "late":
		// Ready, then wait for the host to make the masked paths.
		marks := os.Getenv("MARK_DIR")
		if err := os.WriteFile(filepath.Join(marks, "ready"), nil, 0o600); err != nil {
			return check(false, "ready: %v", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(marks, "made")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				return check(false, "the host never made the paths")
			}
			time.Sleep(10 * time.Millisecond)
		}
		for _, p := range strings.Split(os.Getenv("LATE_PATHS"), ":") {
			if _, err := os.Stat(p); err == nil {
				return check(false, "%s, made after the start, is visible", p)
			}
		}
		b, err := os.ReadFile(os.Getenv("SIBLING"))
		if err != nil || string(b) != "sib" {
			return check(false, "a sibling reads %q, %v", b, err)
		}
		b, err = os.ReadFile(os.Getenv("SIBLING_LINK"))
		return check(err == nil && string(b) == "sib", "a sibling link reads %q, %v", b, err)
	case "masked-socket":
		c, err := net.DialTimeout("unix", os.Getenv("MASKED_SOCKET"), time.Second)
		if c != nil {
			_ = c.Close()
		}
		return check(err != nil, "a masked socket still connects")
	case "submount":
		info, err := os.ReadFile("/proc/self/mountinfo")
		if err != nil {
			return check(false, "mountinfo: %v", err)
		}
		return check(slices.Contains(mountPoints(string(info)), os.Getenv("SUBMOUNT")), "%s is no longer a mount inside", os.Getenv("SUBMOUNT"))
	case "masked":
		b, err := os.ReadFile(os.Getenv("MASKED_FILE"))
		if err != nil || len(b) != 0 {
			return check(false, "masked file reads %q, %v", b, err)
		}
		ents, err := os.ReadDir(os.Getenv("MASKED_DIR"))
		if err != nil || len(ents) != 0 {
			return check(false, "masked dir lists %d, %v", len(ents), err)
		}
		b, err = os.ReadFile(os.Getenv("OPEN_FILE"))
		return check(err == nil && string(b) == "visible", "an unmasked file reads %q, %v", b, err)
	case "identity":
		want, _ := strconv.Atoi(os.Getenv("WANT_UID"))
		if os.Getuid() != want {
			return check(false, "uid %d, want %d", os.Getuid(), want)
		}
		status, err := os.ReadFile("/proc/self/status")
		if err != nil {
			return check(false, "status: %v", err)
		}
		for _, l := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(l, "CapEff:") {
				return check(strings.TrimSpace(strings.TrimPrefix(l, "CapEff:")) == "0000000000000000", "capabilities: %s", l)
			}
		}
		return check(false, "no CapEff line")
	case "processes":
		ents, err := os.ReadDir("/proc")
		if err != nil {
			return check(false, "proc: %v", err)
		}
		var pids []string
		for _, e := range ents {
			if _, err := strconv.Atoi(e.Name()); err == nil {
				pids = append(pids, e.Name())
			}
		}
		host := os.Getenv("HOST_PID")
		for _, p := range pids {
			if p == host {
				return check(false, "the host's test process is visible")
			}
		}
		return check(len(pids) <= 3, "sees %d processes: %v", len(pids), pids)
	case "env":
		return check(os.Getenv(stageEnv) == "", "the stage spec reached the command")
	case "exit7":
		return 7
	}
	return check(false, "unknown act %q", act)
}

// available skips only where the kernel's own settings rule unprivileged user
// namespaces out. Where they allow them, a failing probe is a failure: the
// probe is what is under test, so it cannot also decide whether to test.
func available(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	if why := kernelForbids(); why != "" {
		t.Skipf("unprivileged user namespaces are off here: %s", why)
	}
	require.NoError(t, Available(self), "the kernel allows unprivileged user namespaces, so the probe must pass")
	return self
}

func kernelForbids() string {
	for file, bad := range map[string]string{
		"/proc/sys/kernel/apparmor_restrict_unprivileged_userns": "1",
		"/proc/sys/kernel/unprivileged_userns_clone":             "0",
		"/proc/sys/user/max_user_namespaces":                     "0",
	} {
		if b, err := os.ReadFile(file); err == nil && strings.TrimSpace(string(b)) == bad {
			return file + " is " + bad
		}
	}
	return ""
}

// proxy stands in for the egress proxy: it answers "pong" to a line.
func proxy(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "proxy.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				if _, err := bufio.NewReader(c).ReadString('\n'); err == nil {
					_, _ = fmt.Fprintln(c, "pong")
				}
			}()
		}
	}()
	return sock
}

func run(t *testing.T, self string, s Spec, act string, env ...string) int {
	t.Helper()
	t.Setenv(actEnv, act)
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	s.Command = []string{self}
	code, err := Run(self, s)
	require.NoError(t, err)
	return code
}

func TestConfined(t *testing.T) {
	self := available(t)
	sock := proxy(t)
	dir := t.TempDir()
	maskedDir := filepath.Join(dir, "secrets")
	require.NoError(t, os.Mkdir(maskedDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(maskedDir, "token"), []byte("real"), 0o600))
	maskedFile := filepath.Join(dir, "host.sock-standin")
	require.NoError(t, os.WriteFile(maskedFile, []byte("hidden"), 0o600))
	openFile := filepath.Join(dir, "open")
	require.NoError(t, os.WriteFile(openFile, []byte("visible"), 0o600))
	spec := Spec{Socket: sock, Mask: []string{maskedDir, maskedFile, filepath.Join(dir, "missing")}}

	t.Run("nothing leaves but through the bridge", func(t *testing.T) {
		assert.Equal(t, 0, run(t, self, spec, "no-route"))
	})
	t.Run("the bridge reaches the proxy", func(t *testing.T) {
		assert.Equal(t, 0, run(t, self, spec, "bridge"))
	})
	t.Run("a forward reaches its socket", func(t *testing.T) {
		fwd := spec
		fwd.Forwards = []Forward{{Addr: "127.0.0.1:7777", Socket: proxy(t)}}
		assert.Equal(t, 0, run(t, self, fwd, "forward", "FORWARD_ADDR=127.0.0.1:7777"))
	})
	t.Run("a kept path stays visible inside a masked dir", func(t *testing.T) {
		state := t.TempDir()
		sock := filepath.Join(state, "gate.sock")
		ln, err := net.Listen("unix", sock)
		require.NoError(t, err)
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() {
					defer func() { _ = c.Close() }()
					if _, err := bufio.NewReader(c).ReadString('\n'); err == nil {
						_, _ = fmt.Fprintln(c, "pong")
					}
				}()
			}
		}()
		own := filepath.Join(state, "temp", "agent-1")
		require.NoError(t, os.MkdirAll(own, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(own, "f"), []byte("mine"), 0o600))
		other := filepath.Join(state, "mesh.key")
		require.NoError(t, os.WriteFile(other, []byte("node key"), 0o600))
		kept := Spec{Socket: sock, Mask: []string{state}, Keep: []string{sock, own, filepath.Join(state, "absent")},
			Forwards: []Forward{{Addr: "127.0.0.1:7778", Socket: sock}}}
		assert.Equal(t, 0, run(t, self, kept, "kept", "HIDDEN_FILE="+other, "KEPT_FILE="+filepath.Join(own, "f"), "FORWARD_ADDR=127.0.0.1:7778"))
	})
	t.Run("masked paths are hidden and the rest is not", func(t *testing.T) {
		assert.Equal(t, 0, run(t, self, spec, "masked", "MASKED_DIR="+maskedDir, "MASKED_FILE="+maskedFile, "OPEN_FILE="+openFile))
		b, err := os.ReadFile(filepath.Join(maskedDir, "token"))
		require.NoError(t, err)
		assert.Equal(t, "real", string(b), "the host's own view is untouched")
	})
	t.Run("a masked path made after the start stays unseen", func(t *testing.T) {
		late := func(t *testing.T, masked bool) int {
			parent := t.TempDir()
			marks := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(parent, "sib"), []byte("sib"), 0o600))
			require.NoError(t, os.Symlink("sib", filepath.Join(parent, "lnk")))
			sock := filepath.Join(parent, "late.sock")
			deep := filepath.Join(parent, "nodir", "late.sock")
			// A masked link whose target, elsewhere, does not exist yet.
			elsewhere := filepath.Join(t.TempDir(), "target.sock")
			dangling := filepath.Join(parent, "dangling")
			require.NoError(t, os.Symlink(elsewhere, dangling))
			s := spec
			if masked {
				s.Mask = append(append([]string{}, spec.Mask...), sock, deep, dangling)
			}
			t.Setenv("MARK_DIR", marks)
			t.Setenv("LATE_PATHS", sock+":"+deep+":"+dangling)
			t.Setenv("SIBLING", filepath.Join(parent, "sib"))
			t.Setenv("SIBLING_LINK", filepath.Join(parent, "lnk"))
			t.Setenv(actEnv, "late")
			s.Command = []string{self}
			done := make(chan int, 1)
			go func() {
				code, err := Run(self, s)
				assert.NoError(t, err)
				done <- code
			}()
			require.Eventually(t, func() bool {
				_, err := os.Stat(filepath.Join(marks, "ready"))
				return err == nil
			}, 10*time.Second, 10*time.Millisecond)
			ln, err := net.Listen("unix", sock)
			require.NoError(t, err)
			t.Cleanup(func() { _ = ln.Close() })
			require.NoError(t, os.Mkdir(filepath.Dir(deep), 0o700))
			require.NoError(t, os.WriteFile(deep, nil, 0o600))
			require.NoError(t, os.WriteFile(elsewhere, nil, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(marks, "made"), nil, 0o600))
			return <-done
		}
		assert.Equal(t, 0, late(t, true))
		assert.NotEqual(t, 0, late(t, false), "unmasked, the late socket shows: the check can fail")
	})
	t.Run("a masked socket cannot be reached", func(t *testing.T) {
		live := proxy(t)
		c, err := net.Dial("unix", live)
		require.NoError(t, err, "the socket answers on the host")
		_ = c.Close()
		masked := spec
		masked.Mask = append(append([]string{}, spec.Mask...), live)
		assert.Equal(t, 0, run(t, self, masked, "masked-socket", "MASKED_SOCKET="+live))
		assert.NotEqual(t, 0, run(t, self, spec, "masked-socket", "MASKED_SOCKET="+live), "unmasked, it connects: the check can fail")
	})
	t.Run("a shadowed directory keeps the mounts below it", func(t *testing.T) {
		info, err := os.ReadFile("/proc/self/mountinfo")
		require.NoError(t, err)
		var sub string
		for _, m := range mountPoints(string(info)) {
			if strings.HasPrefix(m, "/run/") {
				sub = m
				break
			}
		}
		if sub == "" {
			t.Skip("no mount below /run on this host to check")
		}
		// A missing path in /run shadows /run itself, inside only.
		shadowed := spec
		shadowed.Mask = append(append([]string{}, spec.Mask...), "/run/pao-confine-test-missing")
		assert.Equal(t, 0, run(t, self, shadowed, "submount", "SUBMOUNT="+sub))
	})
	t.Run("the command runs as the caller, with no capability", func(t *testing.T) {
		assert.Equal(t, 0, run(t, self, spec, "identity", "WANT_UID="+strconv.Itoa(os.Getuid())))
	})
	t.Run("the command sees only its own processes", func(t *testing.T) {
		assert.Equal(t, 0, run(t, self, spec, "processes", "HOST_PID="+strconv.Itoa(os.Getpid())))
	})
	t.Run("the stage spec does not reach the command", func(t *testing.T) {
		assert.Equal(t, 0, run(t, self, spec, "env"))
	})
	t.Run("the command's exit status comes back", func(t *testing.T) {
		assert.Equal(t, 7, run(t, self, spec, "exit7"))
	})
}

func TestRunNeedsACommandAndASocket(t *testing.T) {
	_, err := Run("/bin/true", Spec{Socket: "/x"})
	assert.Error(t, err)
	_, err = Run("/bin/true", Spec{Command: []string{"true"}})
	assert.Error(t, err)
	for _, f := range []Forward{{Addr: "10.0.0.1:80", Socket: "/x"}, {Addr: BridgeAddr, Socket: "/x"}, {Addr: "127.0.0.1:80"}, {Addr: "nope", Socket: "/x"}} {
		_, err = Run("/bin/true", Spec{Socket: "/x", Command: []string{"true"}, Forwards: []Forward{f}})
		assert.Error(t, err, "%+v", f)
	}
}

// mountPoints lists the mount points in a mountinfo text.
func mountPoints(info string) []string {
	var out []string
	for _, l := range strings.Split(info, "\n") {
		if f := strings.Fields(l); len(f) > 4 {
			out = append(out, f[4])
		}
	}
	return out
}
