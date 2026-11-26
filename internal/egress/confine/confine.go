//go:build linux

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
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// stageEnv carries the spec to the second step; it is removed before the
// command starts.
const stageEnv = "PAO_CONFINE_STAGE"

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
	for _, f := range s.Forwards {
		host, _, err := net.SplitHostPort(f.Addr)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return 0, fmt.Errorf("confine: forward %q is not a loopback address", f.Addr)
		}
		if f.Addr == BridgeAddr || f.Socket == "" {
			return 0, fmt.Errorf("confine: forward %q", f.Addr)
		}
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
	for _, f := range append([]Forward{{Addr: BridgeAddr, Socket: st.Socket}}, st.Forwards...) {
		ln, err := net.Listen("tcp", f.Addr)
		if err != nil {
			return fail("bridge", err)
		}
		go bridge(ln, f.Socket)
	}
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
	// A kept path is held open before the masks hide it, and mounted back
	// over its own place afterwards.
	type held struct {
		path string
		fd   int
		dir  bool
	}
	var keeps []held
	defer func() {
		for _, k := range keeps {
			_ = unix.Close(k.fd)
		}
	}()
	for _, p := range s.Keep {
		fd, err := unix.Open(p, unix.O_PATH|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return fmt.Errorf("keep %s: %w", p, err)
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			_ = unix.Close(fd)
			return fmt.Errorf("keep %s: %w", p, err)
		}
		keeps = append(keeps, held{path: p, fd: fd, dir: st.Mode&unix.S_IFMT == unix.S_IFDIR})
	}
	// A masked path that does not exist yet must not show if it is made
	// while the command runs, so its directory is shadowed first; the masks
	// of paths that exist go on top.
	if err := shadowMissing(s.Mask); err != nil {
		return err
	}
	for _, p := range s.Mask {
		if err := mask(p); err != nil {
			return fmt.Errorf("mask %s: %w", p, err)
		}
	}
	for _, k := range keeps {
		if err := putBack(k.path, k.fd, k.dir, unix.MS_BIND); err != nil {
			return fmt.Errorf("keep %s: %w", k.path, err)
		}
	}
	return loopbackUp()
}

// shadowMissing covers, for every masked path that does not exist, the
// nearest directory above it that does: an empty tmpfs goes over that
// directory, and everything already in it is put back except the missing
// name. Whatever the host makes there later, the socket a service starts
// once the command is running among it, stays out of sight.
func shadowMissing(paths []string) error {
	hide := map[string]map[string]bool{}
	var order []string
	for _, p := range paths {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		dir, name := filepath.Dir(p), filepath.Base(p)
		for {
			if _, err := os.Stat(dir); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("mask %s: %w", p, err)
			}
			dir, name = filepath.Dir(dir), filepath.Base(dir)
		}
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return fmt.Errorf("mask %s: %w", p, err)
		}
		if real == "/" {
			return fmt.Errorf("mask %s: it would shadow the root", p)
		}
		if hide[real] == nil {
			hide[real] = map[string]bool{}
			order = append(order, real)
		}
		hide[real][name] = true
	}
	for _, dir := range order {
		if err := shadow(dir, hide[dir]); err != nil {
			return fmt.Errorf("shadow %s: %w", dir, err)
		}
	}
	return nil
}

// shadow puts an empty tmpfs over dir and its entries back on it, all but the
// hidden names: a directory, file or socket is mounted back from a handle held
// across the tmpfs, and a symbolic link is made again.
func shadow(dir string, hide map[string]bool) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type held struct {
		path, link string
		fd         int
		dir        bool
	}
	var back []held
	defer func() {
		for _, h := range back {
			if h.fd >= 0 {
				_ = unix.Close(h.fd)
			}
		}
	}()
	for _, e := range ents {
		path := filepath.Join(dir, e.Name())
		if hide[e.Name()] {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			back = append(back, held{path: path, link: link, fd: -1})
			continue
		}
		fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return err
		}
		back = append(back, held{path: path, fd: fd, dir: e.IsDir()})
	}
	mode := fi.Mode().Perm()
	if fi.Mode()&os.ModeSticky != 0 {
		mode |= 0o1000
	}
	if err := unix.Mount("tmpfs", dir, "tmpfs", unix.MS_NOSUID, fmt.Sprintf("mode=%o", mode)); err != nil {
		return err
	}
	for _, h := range back {
		if h.fd < 0 {
			if err := os.Symlink(h.link, h.path); err != nil {
				return err
			}
			continue
		}
		// Recursive, so a mount below the entry comes back with it.
		if err := putBack(h.path, h.fd, h.dir, unix.MS_BIND|unix.MS_REC); err != nil {
			return fmt.Errorf("put back %s: %w", h.path, err)
		}
	}
	return nil
}

// putBack mounts the open path fd back at path with flags, making a place for
// it first where a mask or a shadow left none.
func putBack(path string, fd int, dir bool, flags uintptr) error {
	if dir {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
		if err != nil && !errors.Is(err, unix.ENXIO) {
			return err
		}
		if f != nil {
			_ = f.Close()
		}
	}
	return unix.Mount(fmt.Sprintf("/proc/self/fd/%d", fd), path, "", flags, "")
}

func mask(p string) error {
	fi, err := os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		// Shadowed already: see shadowMissing.
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
