package helperpool

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// Spec describes a helper to start.
type Spec struct {
	// Bin is the path to the darak-helper binary.
	Bin string
	// Root is the directory the helper serves. Every path in the protocol is
	// relative to it, and RESOLVE_BENEATH keeps resolution inside it.
	Root string

	UID uint32
	GID uint32
	// Groups is the user's complete supplementary group set.
	//
	// It is passed explicitly rather than left to the child to look up, because
	// the set is then a value the server holds and can compare. A helper keeps
	// the groups it started with for its whole life, so the server has to notice
	// a team change and restart it — which it can only do if it knows what the
	// running helper was given.
	Groups []uint32
}

// Helper is a running helper process and the client attached to it.
type Helper struct {
	*Client
	cmd *exec.Cmd

	// groups is what this process was started with, so a caller can tell whether
	// a membership change has left it stale.
	groups []uint32
}

// Groups returns the supplementary group set this helper was started with.
func (h *Helper) Groups() []uint32 { return append([]uint32(nil), h.groups...) }

// Stop closes the connection and waits for the process to exit.
func (h *Helper) Stop() error {
	err := h.Client.Close()
	// Closing the socket ends Serve, so the child exits on its own; kill only if
	// it does not.
	if werr := h.cmd.Wait(); werr != nil && err == nil {
		err = werr
	}
	return err
}

// Spawn starts a helper as the given user and returns a client for it.
//
// The privilege drop uses the child's credentials rather than setpriv: it is one
// less runtime dependency, it happens between fork and exec where it cannot
// affect this process, and it takes the supplementary groups as an explicit list
// instead of having the child re-derive them.
func Spawn(spec Spec) (*Helper, error) {
	if spec.UID == 0 || spec.GID == 0 {
		// A helper running as root would defeat the entire arrangement: every
		// permission check it performs would pass.
		return nil, fmt.Errorf("helperpool: refusing to spawn a helper as uid %d/gid %d", spec.UID, spec.GID)
	}

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("helperpool: socketpair: %w", err)
	}
	childEnd := os.NewFile(uintptr(fds[0]), "helper-child")
	parentEnd := os.NewFile(uintptr(fds[1]), "helper-parent")
	defer childEnd.Close()

	// A socketpair has no filesystem presence, so there is no path to protect and
	// nothing for another process on the host to connect to.
	cmd := exec.Command(spec.Bin, spec.Root)
	cmd.ExtraFiles = []*os.File{childEnd} // becomes fd 3 in the child
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:    spec.UID,
			Gid:    spec.GID,
			Groups: spec.Groups,
		},
		// Without this the child keeps the parent's session and would survive a
		// server restart holding an open socket nobody reads.
		Setsid: true,
	}
	if err := cmd.Start(); err != nil {
		parentEnd.Close()
		return nil, fmt.Errorf("helperpool: start helper for uid %d: %w", spec.UID, err)
	}

	conn, err := net.FileConn(parentEnd)
	parentEnd.Close()
	if err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("helperpool: attach to helper: %w", err)
	}
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("helperpool: socket is not a unix socket")
	}

	return &Helper{
		Client: NewClient(uc),
		cmd:    cmd,
		groups: append([]uint32(nil), spec.Groups...),
	}, nil
}
