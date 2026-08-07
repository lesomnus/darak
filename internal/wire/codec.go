package wire

import (
	"encoding/binary"
	"errors"
	"math"
)

var (
	// ErrShort means a message ended in the middle of a field. Decoding never
	// panics on it: the frames the root-side server decodes are produced by an
	// unprivileged process, so a malformed one is expected input, not a bug.
	ErrShort = errors.New("wire: frame truncated")
	// ErrTrailing means bytes were left over. A well-formed peer sends none, and
	// silently ignoring them would let a message smuggle a second meaning past a
	// decoder that only read the first.
	ErrTrailing = errors.New("wire: trailing bytes after message")
	// ErrTooLarge means the encoded message would not fit MaxFrame.
	ErrTooLarge = errors.New("wire: message exceeds MaxFrame")
	// ErrBadOp means the op byte is not a known operation.
	ErrBadOp = errors.New("wire: unknown op")
	// ErrFieldTooLong means a string or byte field cannot be represented.
	ErrFieldTooLong = errors.New("wire: field too long")
	// ErrBadFlags means a response carries a bit that is not defined. Both ends
	// ship together, so there is no forward compatibility to preserve and an
	// undefined bit is either a bug or a peer carrying state we never agreed to.
	ErrBadFlags = errors.New("wire: unknown response flag bit")
)

// --- encoding ---

func appendStr(b []byte, s string) ([]byte, error) {
	if len(s) > math.MaxUint16 {
		return nil, ErrFieldTooLong
	}
	b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
	return append(b, s...), nil
}

func appendBytes(b []byte, v []byte) ([]byte, error) {
	if uint64(len(v)) > math.MaxUint32 {
		return nil, ErrFieldTooLong
	}
	b = binary.BigEndian.AppendUint32(b, uint32(len(v)))
	return append(b, v...), nil
}

func appendStat(b []byte, s *Stat) []byte {
	b = binary.BigEndian.AppendUint32(b, s.Mode)
	b = binary.BigEndian.AppendUint32(b, s.UID)
	b = binary.BigEndian.AppendUint32(b, s.GID)
	b = binary.BigEndian.AppendUint64(b, uint64(s.Size))
	b = binary.BigEndian.AppendUint64(b, uint64(s.MtimeSec))
	b = binary.BigEndian.AppendUint32(b, s.MtimeNsec)
	b = binary.BigEndian.AppendUint64(b, s.Ino)
	b = binary.BigEndian.AppendUint64(b, s.Nlink)
	return b
}

// Marshal encodes the request into a single frame.
func (r *Request) Marshal() ([]byte, error) {
	if !r.Op.Valid() {
		return nil, ErrBadOp
	}
	b := make([]byte, 0, 64+len(r.Path)+len(r.Path2)+len(r.Name)+len(r.Value))
	b = binary.BigEndian.AppendUint64(b, r.ID)
	b = append(b, byte(r.Op))
	b = binary.BigEndian.AppendUint32(b, r.Flags)
	b = binary.BigEndian.AppendUint32(b, r.Mode)

	var err error
	for _, s := range []string{r.Path, r.Path2, r.Name} {
		if b, err = appendStr(b, s); err != nil {
			return nil, err
		}
	}
	if b, err = appendBytes(b, r.Value); err != nil {
		return nil, err
	}
	if len(b) > MaxFrame {
		return nil, ErrTooLarge
	}
	return b, nil
}

// Marshal encodes the response into a single frame. Any descriptor is sent
// separately as SCM_RIGHTS; only the HasFD bit appears here.
func (r *Response) Marshal() ([]byte, error) {
	b := make([]byte, 0, 64)
	b = binary.BigEndian.AppendUint64(b, r.ID)
	b = binary.BigEndian.AppendUint32(b, r.Errno)

	if r.Has&^hasKnown != 0 {
		return nil, ErrBadFlags
	}
	// Keep the bits honest: a reader must be able to trust that HasStat means a
	// Stat really follows, since that is what drives its parsing.
	has := r.Has
	if r.Stat != nil {
		has |= HasStat
	} else {
		has &^= HasStat
	}
	// HasEntries is only ever ADDED here, never cleared. An empty directory is a
	// listing of zero entries, and clearing the bit would make it indistinguish-
	// able from a response that carries no listing at all.
	if len(r.Entries) > 0 {
		has |= HasEntries
	}
	b = append(b, has)

	if has&HasStat != 0 {
		b = appendStat(b, r.Stat)
	}
	if has&HasEntries != 0 {
		if len(r.Entries) > math.MaxUint32 {
			return nil, ErrFieldTooLong
		}
		b = binary.BigEndian.AppendUint32(b, uint32(len(r.Entries)))
		for i := range r.Entries {
			e := &r.Entries[i]
			var err error
			if b, err = appendStr(b, e.Name); err != nil {
				return nil, err
			}
			b = append(b, e.Type)
			if e.Stat != nil {
				b = append(b, 1)
				b = appendStat(b, e.Stat)
			} else {
				b = append(b, 0)
			}
		}
	}
	if len(b) > MaxFrame {
		return nil, ErrTooLarge
	}
	return b, nil
}

// --- decoding ---

// cursor reads fields left to right, latching the first error. Every accessor
// bounds-checks and returns a zero value once the cursor has failed, so a
// truncated or hostile frame walks off the end harmlessly instead of panicking.
type cursor struct {
	b   []byte
	err error
}

func (c *cursor) take(n int) []byte {
	if c.err != nil {
		return nil
	}
	if n < 0 || n > len(c.b) {
		c.err = ErrShort
		return nil
	}
	v := c.b[:n]
	c.b = c.b[n:]
	return v
}

func (c *cursor) u8() uint8 {
	v := c.take(1)
	if v == nil {
		return 0
	}
	return v[0]
}

func (c *cursor) u16() uint16 {
	v := c.take(2)
	if v == nil {
		return 0
	}
	return binary.BigEndian.Uint16(v)
}

func (c *cursor) u32() uint32 {
	v := c.take(4)
	if v == nil {
		return 0
	}
	return binary.BigEndian.Uint32(v)
}

func (c *cursor) u64() uint64 {
	v := c.take(8)
	if v == nil {
		return 0
	}
	return binary.BigEndian.Uint64(v)
}

func (c *cursor) str() string {
	n := int(c.u16())
	v := c.take(n)
	if v == nil {
		return ""
	}
	return string(v)
}

func (c *cursor) bytes() []byte {
	n := c.u32()
	// Check against what is actually left before converting to int: on a 32-bit
	// build a large length would otherwise overflow into a negative int.
	if c.err == nil && uint64(n) > uint64(len(c.b)) {
		c.err = ErrShort
		return nil
	}
	v := c.take(int(n))
	if v == nil {
		return nil
	}
	return append([]byte(nil), v...)
}

func (c *cursor) stat() *Stat {
	s := &Stat{}
	s.Mode = c.u32()
	s.UID = c.u32()
	s.GID = c.u32()
	s.Size = int64(c.u64())
	s.MtimeSec = int64(c.u64())
	s.MtimeNsec = c.u32()
	s.Ino = c.u64()
	s.Nlink = c.u64()
	if c.err != nil {
		return nil
	}
	return s
}

func (c *cursor) done() error {
	if c.err != nil {
		return c.err
	}
	if len(c.b) != 0 {
		return ErrTrailing
	}
	return nil
}

// UnmarshalRequest decodes one request frame.
func UnmarshalRequest(b []byte) (*Request, error) {
	if len(b) > MaxFrame {
		return nil, ErrTooLarge
	}
	c := &cursor{b: b}
	r := &Request{}
	r.ID = c.u64()
	r.Op = Op(c.u8())
	r.Flags = c.u32()
	r.Mode = c.u32()
	r.Path = c.str()
	r.Path2 = c.str()
	r.Name = c.str()
	r.Value = c.bytes()
	if err := c.done(); err != nil {
		return nil, err
	}
	// Checked after the frame parses so a malformed op is reported as a bad op
	// rather than as whatever the following bytes happen to look like.
	if !r.Op.Valid() {
		return nil, ErrBadOp
	}
	return r, nil
}

// UnmarshalResponse decodes one response frame.
//
// This is the direction that matters: the root-side server decodes frames
// written by a process running as an ordinary user.
func UnmarshalResponse(b []byte) (*Response, error) {
	if len(b) > MaxFrame {
		return nil, ErrTooLarge
	}
	c := &cursor{b: b}
	r := &Response{}
	r.ID = c.u64()
	r.Errno = c.u32()
	r.Has = c.u8()
	if c.err == nil && r.Has&^hasKnown != 0 {
		return nil, ErrBadFlags
	}

	if r.Has&HasStat != 0 {
		r.Stat = c.stat()
	}
	if r.Has&HasEntries != 0 {
		n := c.u32()
		// An entry costs at least three bytes on the wire (2 length + 1 type, for
		// an empty name with no stat). Bounding the count by what is left stops a
		// declared-but-absent four-billion-entry listing from being allocated
		// before the first read fails.
		if c.err == nil && uint64(n) > uint64(len(c.b)/3) {
			return nil, ErrShort
		}
		if c.err == nil && n > 0 {
			r.Entries = make([]DirEntry, 0, n)
		}
		for i := uint32(0); i < n && c.err == nil; i++ {
			e := DirEntry{Name: c.str(), Type: c.u8()}
			// Strictly 0 or 1. Accepting any non-zero byte would make two distinct
			// frames decode to the same value, so a frame could not be checked by
			// re-encoding it.
			switch present := c.u8(); present {
			case 0:
			case 1:
				e.Stat = c.stat()
			default:
				if c.err == nil {
					return nil, ErrBadFlags
				}
			}
			if c.err != nil {
				break
			}
			r.Entries = append(r.Entries, e)
		}
	}
	if err := c.done(); err != nil {
		return nil, err
	}
	return r, nil
}
