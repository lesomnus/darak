// Package helperpool is the server side of the helper boundary: it speaks the
// protocol to one user's helper and, later, owns the set of running helpers.
//
// Everything here returns file descriptors and errnos. It deliberately offers no
// way to ask the helper "may this user do X" separately from doing it, because a
// separate check would be a decision made at a different moment than the access
// — which is the TOCTOU that the whole fd-passing design exists to avoid.
package helperpool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// ErrClosed is returned once the client has been shut down.
var ErrClosed = errors.New("helperpool: client is closed")

type result struct {
	resp *wire.Response
	file *os.File
}

// Client talks to one helper process.
//
// Requests carry an id and replies are matched back to it, so calls do not queue
// behind one another. There is exactly one helper per user, so without that a
// single large upload would hold up that user's every other request and nothing
// could route around it.
type Client struct {
	conn *net.UnixConn

	nextID atomic.Uint64

	mu      sync.Mutex
	pending map[uint64]chan result
	closed  bool
	err     error // why the reader stopped

	done chan struct{}
}

// NewClient starts serving replies from conn. It takes ownership of conn.
func NewClient(conn *net.UnixConn) *Client {
	c := &Client{
		conn:    conn,
		pending: map[uint64]chan result{},
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Close shuts the connection down and fails every in-flight call.
func (c *Client) Close() error {
	err := c.conn.Close()
	<-c.done
	return err
}

func (c *Client) readLoop() {
	defer close(c.done)

	buf := make([]byte, wire.MaxFrame)
	// One descriptor per reply is all the protocol ever sends; size the control
	// buffer for exactly that so a peer cannot make us accept a pile of them.
	oob := make([]byte, unix.CmsgSpace(4))

	for {
		n, oobn, _, _, err := c.conn.ReadMsgUnix(buf, oob)
		if err != nil {
			c.shutdown(err)
			return
		}
		file := parseRights(oob[:oobn])
		resp, err := wire.UnmarshalResponse(buf[:n])
		if err != nil {
			// A frame we cannot parse cannot be matched to a caller, so there is
			// nothing to fail but the connection. Continuing would leave the caller
			// waiting on a reply that already came and was discarded.
			if file != nil {
				file.Close()
			}
			c.shutdown(fmt.Errorf("helperpool: malformed reply: %w", err))
			return
		}
		if (resp.Has&wire.HasFD != 0) != (file != nil) {
			if file != nil {
				file.Close()
			}
			c.shutdown(fmt.Errorf("helperpool: reply %d claims HasFD=%v but carried %d descriptors",
				resp.ID, resp.Has&wire.HasFD != 0, boolToInt(file != nil)))
			return
		}

		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if !ok {
			// A reply to a call that already gave up. Dropping the descriptor here
			// is what keeps a timed-out request from leaking one.
			if file != nil {
				file.Close()
			}
			continue
		}
		ch <- result{resp: resp, file: file}
	}
}

func (c *Client) shutdown(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}

// Do sends one request and waits for its reply.
//
// The returned *os.File is non-nil only when the helper passed a descriptor, and
// the caller owns it. resp.Errno carries the kernel's verdict untranslated: it
// is the answer to "may this user do this", and re-deriving it here would be a
// second permission model disagreeing with the first.
func (c *Client) Do(ctx context.Context, req *wire.Request) (*wire.Response, *os.File, error) {
	req.ID = c.nextID.Add(1)
	frame, err := req.Marshal()
	if err != nil {
		return nil, nil, err
	}

	ch := make(chan result, 1)
	c.mu.Lock()
	if c.closed {
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = ErrClosed
		}
		return nil, nil, err
	}
	c.pending[req.ID] = ch
	c.mu.Unlock()

	if _, _, err := c.conn.WriteMsgUnix(frame, nil, nil); err != nil {
		c.mu.Lock()
		delete(c.pending, req.ID)
		c.mu.Unlock()
		return nil, nil, err
	}

	select {
	case r, ok := <-ch:
		if !ok {
			if c.err != nil {
				return nil, nil, c.err
			}
			return nil, nil, ErrClosed
		}
		return r.resp, r.file, nil

	case <-ctx.Done():
		// Leave the id registered: the reply may still arrive, and readLoop needs
		// to find it in order to close any descriptor that comes with it.
		return nil, nil, ctx.Err()
	}
}

// parseRights extracts at most one descriptor from a control message, closing
// any extras rather than leaking them.
func parseRights(oob []byte) *os.File {
	if len(oob) == 0 {
		return nil
	}
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return nil
	}
	var out *os.File
	for _, m := range msgs {
		fds, err := unix.ParseUnixRights(&m)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if out == nil {
				out = os.NewFile(uintptr(fd), "helper-fd")
				continue
			}
			unix.Close(fd)
		}
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
