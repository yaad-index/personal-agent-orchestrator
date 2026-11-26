// Package confine runs a command where its only way out is the egress proxy
// (ADR-0049): its own network, holding only loopback and a bridge to the
// proxy's socket, its own process table, and its own view of the filesystem
// with chosen paths hidden. It needs no privilege beyond an unprivileged user
// namespace; where the host does not allow one, Available says so and nothing
// is run unconfined.
//
// It runs in three steps. Run, on the host, starts this same program again as
// the first process of new user, network, mount and pid namespaces, mapped to
// root there so it can set them up. That second step brings loopback up,
// mounts a /proc for the new process table, hides the masked paths, starts the
// bridge, and then starts the command in one more user namespace, mapped back
// to the caller's own uid: the command runs as the caller, with no
// capability, and stays in the network, mount and pid namespaces set up for
// it. The second step stays to run the bridge and reap, and exits with the
// command's status.
//
// A network namespace does not cover a Unix socket on the filesystem. A host
// socket the command can reach, and that would act on the host for it (a
// terminal multiplexer's, a user service manager's, a container engine's), is
// a way out, so such a path belongs in Mask.
package confine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// BridgeAddr is where, inside, the command reaches the proxy.
const BridgeAddr = "127.0.0.1:3128"

// stageEnv carries the spec to the second step; it is removed before the
// command starts.
const stageEnv = "PAO_CONFINE_STAGE"

// Spec is what a confined command runs under.
type Spec struct {
	// Socket is the proxy's socket on the host; the bridge connects to it.
	Socket string `json:"socket"`
	// Mask lists host paths the command does not see: a directory shows
	// empty, a file shows as empty, a missing path is left alone.
	Mask []string `json:"mask"`
	// Command is the program and its arguments.
	Command []string `json:"command"`
}

type stage struct {
	Spec
	UID  int  `json:"uid"`
	GID  int  `json:"gid"`
	Leaf bool `json:"leaf,omitempty"`
}

// IsInner reports whether this process is the second step. A program that
// uses Run checks it first thing in main and, if so, exits with Inner's status.
func IsInner() bool { return os.Getenv(stageEnv) != "" }

// Run starts s confined and returns the command's exit status. self is this
// program's own executable, which must call Inner when IsInner.
func Run(self string, s Spec) (int, error) {
	if len(s.Command) == 0 {
		return 0, errors.New("confine: no command")
	}
	if s.Socket == "" {
		return 0, errors.New("confine: no proxy socket")
	}
	return start(self, stage{Spec: s, UID: os.Getuid(), GID: os.Getgid()})
}

// Available reports whether this host can confine a command, by confining one
// that exits at once.
func Available(self string) error {
	dir, err := os.MkdirTemp("", "pao-confine-probe")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	code, err := start(self, stage{
		Spec: Spec{Socket: dir + "/none", Command: []string{self}},
		UID:  os.Getuid(), GID: os.Getgid(), Leaf: true,
	})
	if err != nil {
		return fmt.Errorf("this host cannot confine a command: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("this host cannot confine a command: the probe exited %d", code)
	}
	return nil
}

func start(self string, st stage) (int, error) {
	b, err := json.Marshal(st)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), stageEnv+"="+string(b))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: st.UID, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: st.GID, Size: 1}},
		GidMappingsEnableSetgroups: false,
		// The second step is the namespace's first process: when it dies, so
		// does everything confined. It dies with this one.
		Pdeathsig: syscall.SIGKILL,
	}
	// The parent-death signal follows the thread that started the child.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	sigs := make(chan os.Signal, 4)
	// Caught, not ignored: an ignored signal would stay ignored in the command.
	// An interrupt from the terminal reaches the command directly.
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go forward(sigs, cmd.Process)
	return exitStatus(cmd.Wait())
}

func forward(sigs <-chan os.Signal, p *os.Process) {
	for s := range sigs {
		if s == syscall.SIGTERM || s == syscall.SIGHUP {
			_ = p.Signal(s)
		}
	}
}

func exitStatus(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal()), nil
		}
		return ee.ExitCode(), nil
	}
	return 0, err
}

// Inner is the second step. It returns the status to exit with.
func Inner() int {
	var st stage
	err := json.Unmarshal([]byte(os.Getenv(stageEnv)), &st)
	_ = os.Unsetenv(stageEnv)
	if err != nil {
		return fail("spec", err)
	}
	if st.Leaf && os.Getpid() != 1 {
		// The probe's own command: confined, it has nothing more to do.
		return 0
	}
	if err := setup(st.Spec); err != nil {
		return fail("setup", err)
	}
	ln, err := net.Listen("tcp", BridgeAddr)
	if err != nil {
		return fail("bridge", err)
	}
	go bridge(ln, st.Socket)
	env := os.Environ()
	if st.Leaf {
		// The probe's command must not start a probe of its own.
		b, _ := json.Marshal(st)
		env = append(env, stageEnv+"="+string(b))
	}
	return runCommand(st, env)
}

func fail(what string, err error) int {
	_, _ = fmt.Fprintf(os.Stderr, "pao confine: %s: %v\n", what, err)
	return 125
}

// setup readies the namespaces: nothing mounted here reaches the host, the
// process table is the new one, the masked paths are hidden, loopback is up.
func setup(s Spec) error {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	if err := syscall.Mount("proc", "/proc", "proc", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}
	for _, p := range s.Mask {
		if err := mask(p); err != nil {
			return fmt.Errorf("mask %s: %w", p, err)
		}
	}
	return loopbackUp()
}

func mask(p string) error {
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return syscall.Mount("tmpfs", p, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV|syscall.MS_NOEXEC, "size=4k,mode=0755")
	}
	return syscall.Mount("/dev/null", p, "", syscall.MS_BIND, "")
}

func loopbackUp() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = syscall.Close(fd) }()
	var ifr struct {
		name  [syscall.IFNAMSIZ]byte
		flags uint16
		_     [22]byte
	}
	copy(ifr.name[:], "lo")
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return fmt.Errorf("read loopback flags: %w", e)
	}
	ifr.flags |= syscall.IFF_UP
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return fmt.Errorf("bring loopback up: %w", e)
	}
	return nil
}

// bridge carries each connection made inside to the proxy's socket.
func bridge(ln net.Listener, socket string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = c.Close() }()
			up, err := net.Dial("unix", socket)
			if err != nil {
				return
			}
			defer func() { _ = up.Close() }()
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = io.Copy(up, c)
				if u, ok := up.(*net.UnixConn); ok {
					_ = u.CloseWrite()
				}
			}()
			_, _ = io.Copy(c, up)
			if t, ok := c.(*net.TCPConn); ok {
				_ = t.CloseWrite()
			}
			wg.Wait()
		}()
	}
}

// runCommand starts the command as the caller's own uid, without capability,
// and reaps every process until the command exits.
func runCommand(st stage, env []string) int {
	path, err := exec.LookPath(st.Command[0])
	if err != nil {
		return fail("command", err)
	}
	cmd := exec.Command(path, st.Command[1:]...)
	cmd.Args[0] = st.Command[0]
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: st.UID, HostID: 0, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: st.GID, HostID: 0, Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	if err := cmd.Start(); err != nil {
		return fail("start", err)
	}
	pid := cmd.Process.Pid
	go forward(sigs, cmd.Process)
	// As the first process here, every orphan is ours to reap; the command's
	// own status is the one that counts.
	for {
		var ws syscall.WaitStatus
		got, err := syscall.Wait4(-1, &ws, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return fail("wait", err)
		}
		if got != pid {
			continue
		}
		if ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ws.ExitStatus()
	}
}
