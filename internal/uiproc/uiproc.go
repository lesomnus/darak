// Package uiproc is how the API server reaches the interface without serving it.
//
// The server runs as root, and a root process that resolves paths does so with
// every permission check passing (internal/lint). Serving the interface means
// resolving a request's path in a directory, so the server does not do it:
// either it starts `darak ui` as an unprivileged child (Supervisor), or the
// interface is deployed on its own and the server is pointed at it (Proxy).
// Either way the server stays the one origin the browser talks to, which is
// what makes the session cookie work, and it forwards nothing the interface has
// no use for.
package uiproc

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Proxy forwards to the interface at target.
//
// ready, when not nil, is asked first; a false answer is a 503 straight away
// rather than a request that waits on a process that is not there.
//
// The request reaches the interface without its cookies or Authorization: the
// interface is static files, and the session is the one thing in the request
// it must never be able to see.
func Proxy(target *url.URL, ready func() bool) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// Host is the browser's, as with the API, so that nothing the
			// interface derives from it names a loopback address.
			r.Out.Host = r.In.Host
			r.Out.Header.Del("Cookie")
			r.Out.Header.Del("Authorization")
			r.Out.Header.Del("Proxy-Authorization")
		},
		ModifyResponse: func(resp *http.Response) error {
			// Nothing the interface says may set a cookie on the server's origin.
			resp.Header.Del("Set-Cookie")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("the interface did not answer", "err", err, "path", r.URL.Path)
			http.Error(w, "interface unavailable", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ready != nil && !ready() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "interface starting", http.StatusServiceUnavailable)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// Creds is who the child runs as.
type Creds struct {
	UID, GID uint32
}

// ParseUser reads -ui-user: a name the system knows, or uid:gid.
//
// Root is refused here as well as by the child itself, so the mistake is named
// at the flag rather than at the first restart.
func ParseUser(s string) (Creds, error) {
	var c Creds
	if uidS, gidS, ok := strings.Cut(s, ":"); ok {
		uid, err := strconv.ParseUint(uidS, 10, 32)
		if err != nil {
			return c, fmt.Errorf("ui user %q: uid: %w", s, err)
		}
		gid, err := strconv.ParseUint(gidS, 10, 32)
		if err != nil {
			return c, fmt.Errorf("ui user %q: gid: %w", s, err)
		}
		c = Creds{UID: uint32(uid), GID: uint32(gid)}
	} else {
		u, err := user.Lookup(s)
		if err != nil {
			return c, fmt.Errorf("ui user %q: %w", s, err)
		}
		uid, err := strconv.ParseUint(u.Uid, 10, 32)
		if err != nil {
			return c, fmt.Errorf("ui user %q: uid %q: %w", s, u.Uid, err)
		}
		gid, err := strconv.ParseUint(u.Gid, 10, 32)
		if err != nil {
			return c, fmt.Errorf("ui user %q: gid %q: %w", s, u.Gid, err)
		}
		c = Creds{UID: uint32(uid), GID: uint32(gid)}
	}
	if c.UID == 0 || c.GID == 0 {
		return c, fmt.Errorf("ui user %q: refusing uid %d/gid %d; the interface must not run as root", s, c.UID, c.GID)
	}
	return c, nil
}

// Config is what a Supervisor starts.
type Config struct {
	// Bin is the darak binary; the child is `Bin ui -dir Dir -listen-fd 3`.
	Bin   string
	Dir   string
	Creds Creds

	// Backoff bounds the wait between restarts. Zero values take the defaults.
	MinBackoff, MaxBackoff time.Duration
}

const (
	DefaultMinBackoff = 200 * time.Millisecond
	DefaultMaxBackoff = 30 * time.Second

	// A child that stayed up this long is treated as healthy: its exit starts the
	// backoff over instead of continuing a crash loop's.
	stableAfter = time.Minute
	stopGrace   = 10 * time.Second
)

// Supervisor keeps one `darak ui` child running and proxies to it.
//
// The listening socket belongs to the supervisor, not the child. It is opened
// once on loopback and handed to every child as fd 3, so the child chooses no
// address and a restart changes none: connections that arrive while a child is
// being replaced wait in the backlog instead of being refused.
type Supervisor struct {
	cfg     Config
	ln      *net.TCPListener
	lnFile  *os.File
	target  *url.URL
	running atomic.Bool

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Start opens the socket and starts the first child. It returns once the child
// has been started, not once it is serving; until then requests queue.
func Start(cfg Config) (*Supervisor, error) {
	if cfg.Creds.UID == 0 || cfg.Creds.GID == 0 {
		return nil, fmt.Errorf("uiproc: refusing to start the interface as uid %d/gid %d", cfg.Creds.UID, cfg.Creds.GID)
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = DefaultMinBackoff
	}
	if cfg.MaxBackoff < cfg.MinBackoff {
		cfg.MaxBackoff = max(DefaultMaxBackoff, cfg.MinBackoff)
	}

	// Loopback, so nothing off this host can reach it; what can reach it from
	// on the host is static files that the server would hand out anyway.
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, fmt.Errorf("uiproc: listen: %w", err)
	}
	f, err := ln.File()
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("uiproc: listener fd: %w", err)
	}
	s := &Supervisor{
		cfg:    cfg,
		ln:     ln,
		lnFile: f,
		target: &url.URL{Scheme: "http", Host: ln.Addr().String()},
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}

	first := make(chan error, 1)
	go s.loop(first)
	if err := <-first; err != nil {
		<-s.done
		s.closeSocket()
		return nil, err
	}
	return s, nil
}

// Handler proxies to the child.
func (s *Supervisor) Handler() http.Handler {
	return Proxy(s.target, s.running.Load)
}

// Close stops the child and waits for it.
func (s *Supervisor) Close() error {
	s.once.Do(func() { close(s.stop) })
	<-s.done
	return s.closeSocket()
}

func (s *Supervisor) closeSocket() error {
	return errors.Join(s.lnFile.Close(), s.ln.Close())
}

// loop runs children until Close. The first start's error goes to first, so a
// binary that cannot even be executed fails the server's startup instead of
// becoming a restart loop nobody is watching.
func (s *Supervisor) loop(first chan<- error) {
	defer close(s.done)
	backoff := s.cfg.MinBackoff
	for {
		started := time.Now()
		err := s.runOne(first)
		if first != nil && errors.Is(err, errStart) {
			return
		}
		first = nil
		if s.stopped() {
			return
		}
		if time.Since(started) > stableAfter {
			backoff = s.cfg.MinBackoff
		}
		slog.Warn("the interface process exited; restarting", "err", err, "in", backoff)
		select {
		case <-s.stop:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, s.cfg.MaxBackoff)
	}
}

var errStart = errors.New("could not start the interface process")

func (s *Supervisor) stopped() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// runOne starts a child and waits for it to exit or for Close.
func (s *Supervisor) runOne(first chan<- error) error {
	// Pdeathsig is delivered when the THREAD that started the child exits, not
	// the process. Holding this goroutine on one thread until the child is gone
	// is what makes it mean "when the server dies".
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	cmd := exec.Command(s.cfg.Bin, "ui", "-dir", s.cfg.Dir, "-listen-fd", "3")
	cmd.ExtraFiles = []*os.File{s.lnFile} // becomes fd 3 in the child
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.Env = []string{} // nothing of the server's environment is the interface's business
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid: s.cfg.Creds.UID,
			Gid: s.cfg.Creds.GID,
			// Explicitly empty: left nil, the child would keep root's
			// supplementary groups.
			Groups: []uint32{},
		},
		Setsid:    true,
		Pdeathsig: syscall.SIGTERM,
	}
	if err := cmd.Start(); err != nil {
		err = fmt.Errorf("%w: %w", errStart, err)
		if first != nil {
			first <- err
		}
		return err
	}
	if first != nil {
		first <- nil
	}
	s.running.Store(true)
	defer s.running.Store(false)
	slog.Info("interface process started", "pid", cmd.Process.Pid, "addr", s.target.Host, "dir", s.cfg.Dir)

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case err := <-exited:
		if err == nil {
			err = errors.New("exited cleanly, which it should only do when told to")
		}
		return err
	case <-s.stop:
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(stopGrace):
			_ = cmd.Process.Kill()
			<-exited
		}
		return nil
	}
}
