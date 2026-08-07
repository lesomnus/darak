// Package helper is the per-user side of the impersonation boundary.
//
// It runs as the requesting user (the server drops it there with setpriv) and
// performs every operation that touches the namespace, because those are checked
// against the CALLING process and would therefore not be checked at all if the
// root server issued them. See docs/helper-protocol.md.
//
// Nothing here decides who may do what. The helper attempts the operation with
// the user's own credentials and reports whatever the kernel says.
package helper

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"

	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// resolveFlags is applied to every path resolution.
//
// RESOLVE_BENEATH makes the kernel refuse anything that leaves the root —
// "..", an absolute path, a symlink pointing outside — which is why no string
// inspection happens anywhere in this package. RESOLVE_NO_MAGICLINKS blocks the
// /proc/*/fd style links that would otherwise be a way back out.
const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS

// Helper serves one user's requests over a SOCK_SEQPACKET socket.
type Helper struct {
	conn   *net.UnixConn
	rootFD int

	// sendMu serializes replies. sendmsg on a SEQPACKET socket is atomic per
	// message, but requests are handled concurrently and this keeps the pairing
	// of a frame with its SCM_RIGHTS descriptor obviously correct rather than
	// merely probably correct.
	sendMu sync.Mutex
}

// New builds a Helper serving root over the socket in sock.
//
// The root directory is opened here, as the user, rather than being inherited
// from the parent: a descriptor handed down by a root process would carry a
// permission decision the user never passed.
func New(root string, sock *os.File) (*Helper, error) {
	// mode is always taken from the request. umask is process-global state, so
	// deriving it from a per-request value would race between concurrent
	// requests — the helper opts out of it entirely.
	unix.Umask(0)

	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: root, Err: err}
	}

	c, err := net.FileConn(sock)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		c.Close()
		unix.Close(fd)
		return nil, errors.New("helper: socket is not a unix socket")
	}
	return &Helper{conn: uc, rootFD: fd}, nil
}

// Close releases the socket and the root descriptor.
func (h *Helper) Close() error {
	err := h.conn.Close()
	if cerr := unix.Close(h.rootFD); err == nil {
		err = cerr
	}
	return err
}

// Serve reads requests until the peer closes the socket.
//
// Each request is handled in its own goroutine and replies carry the request id,
// so responses may be returned out of order. Without that, one large upload
// would sit in front of the same user's directory listing — and since there is
// exactly one helper per user, nothing else could get around it.
func (h *Helper) Serve() error {
	buf := make([]byte, wire.MaxFrame)
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		n, _, _, _, err := h.conn.ReadMsgUnix(buf, nil)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if n == 0 {
			return nil // peer shut down
		}
		frame := append([]byte(nil), buf[:n]...)

		wg.Add(1)
		go func() {
			defer wg.Done()
			h.serveOne(frame)
		}()
	}
}

func (h *Helper) serveOne(frame []byte) {
	req, err := wire.UnmarshalRequest(frame)
	if err != nil {
		// A frame we cannot parse has no id to answer against, so there is nobody
		// to tell. Dropping it is the only honest option; the server times the
		// request out.
		return
	}
	resp, fd := h.dispatch(req)
	h.reply(resp, fd)
}

func (h *Helper) reply(resp *wire.Response, fd int) {
	if fd >= 0 {
		defer unix.Close(fd)
	}
	frame, err := resp.Marshal()
	if err != nil {
		// The only way to fail here is a listing too large to encode, which
		// truncation already prevents; answer with the error rather than nothing.
		frame, err = (&wire.Response{ID: resp.ID, Errno: uint32(unix.EOVERFLOW)}).Marshal()
		if err != nil {
			return
		}
		fd = -1
	}
	var oob []byte
	if fd >= 0 {
		oob = unix.UnixRights(fd)
	}

	h.sendMu.Lock()
	defer h.sendMu.Unlock()
	_, _, _ = h.conn.WriteMsgUnix(frame, oob, nil)
}

// errnoOf maps an error to a raw errno for the response.
func errnoOf(err error) uint32 {
	var errno unix.Errno
	if errors.As(err, &errno) {
		return uint32(errno)
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		if errors.As(pe.Err, &errno) {
			return uint32(errno)
		}
	}
	return uint32(unix.EIO)
}

func fail(id uint64, err error) (*wire.Response, int) {
	return &wire.Response{ID: id, Errno: errnoOf(err)}, -1
}

func ok(id uint64) (*wire.Response, int) {
	return &wire.Response{ID: id}, -1
}
