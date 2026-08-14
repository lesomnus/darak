package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func sampleRequest() *Request {
	return &Request{
		ID:    0xDEADBEEFCAFEF00D,
		Op:    OpRename,
		Flags: FlagNoFollow | FlagWithStat,
		Mode:  0o2770,
		Path:  "teams/design/.upload-abc123",
		Path2: "teams/design/report.xlsx",
		Name:  "user.darak.id",
		Value: []byte{0x00, 0xFF, 0x10, 'x'},
	}
}

func sampleStat() *Stat {
	return &Stat{
		Mode: 0o100660, UID: 3001, GID: 10001,
		Size: 1 << 40, MtimeSec: 1786000000, MtimeNsec: 123456789,
		Ino: 987654321, Nlink: 2,
	}
}

func TestRequestRoundTrip(t *testing.T) {
	want := sampleRequest()
	b, err := want.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := UnmarshalRequest(b)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ID != want.ID || got.Op != want.Op || got.Flags != want.Flags || got.Mode != want.Mode ||
		got.Path != want.Path || got.Path2 != want.Path2 || got.Name != want.Name ||
		!bytes.Equal(got.Value, want.Value) {
		t.Errorf("round trip lost data:\n got %#v\nwant %#v", got, want)
	}
}

// Every op must survive a round trip; a new op added without a name or outside
// the valid range would fail here rather than at runtime.
func TestEveryOpRoundTrips(t *testing.T) {
	for op := OpOpen; op <= opMax; op++ {
		if !op.Valid() {
			t.Fatalf("op %d is in range but reports invalid", op)
		}
		if strings.HasPrefix(op.String(), "Op(") {
			t.Errorf("op %d has no name", op)
		}
		b, err := (&Request{Op: op, Path: "x"}).Marshal()
		if err != nil {
			t.Fatalf("op %s: Marshal: %v", op, err)
		}
		got, err := UnmarshalRequest(b)
		if err != nil {
			t.Fatalf("op %s: Unmarshal: %v", op, err)
		}
		if got.Op != op {
			t.Errorf("op %s decoded as %s", op, got.Op)
		}
	}
}

func TestResponseRoundTrip(t *testing.T) {
	want := &Response{
		ID: 42, Errno: 13, Has: HasFD | HasMore,
		Stat: sampleStat(),
		Entries: []DirEntry{
			{Name: "report.xlsx", Type: 8, Stat: sampleStat()},
			{Name: "notes", Type: 4},
			{Name: "", Type: 0},
		},
	}
	b, err := want.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := UnmarshalResponse(b)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ID != want.ID || got.Errno != want.Errno {
		t.Errorf("header lost: got id=%d errno=%d", got.ID, got.Errno)
	}
	// The out-of-band and continuation bits must survive; the server keys its
	// SCM_RIGHTS handling and its listing resume off them.
	if got.Has&HasFD == 0 || got.Has&HasMore == 0 {
		t.Errorf("flag bits lost: %08b", got.Has)
	}
	if got.Stat == nil || *got.Stat != *want.Stat {
		t.Errorf("stat lost: %#v", got.Stat)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(got.Entries))
	}
	if got.Entries[0].Name != "report.xlsx" || got.Entries[0].Stat == nil || *got.Entries[0].Stat != *want.Stat {
		t.Errorf("entry 0 lost: %#v", got.Entries[0])
	}
	if got.Entries[1].Stat != nil {
		t.Errorf("entry 1 must have no stat, got %#v", got.Entries[1].Stat)
	}
}

// Marshal owns the Has bits so a reader can trust them. If HasStat could be set
// with no Stat behind it, every decoder would have to defend against its own
// peer lying about its own payload.
func TestMarshalCorrectsHasBits(t *testing.T) {
	// Claims a stat it does not have: the bit is dropped, because a nil Stat
	// cannot be encoded and a reader keys its parsing off that bit.
	b, err := (&Response{ID: 1, Has: HasStat}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalResponse(b)
	if err != nil {
		t.Fatalf("a response that over-claims a stat must still encode to something valid: %v", err)
	}
	if got.Has&HasStat != 0 {
		t.Errorf("HasStat should have been cleared, got %08b", got.Has)
	}

	// Carries a stat without setting the bit.
	b, err = (&Response{ID: 1, Stat: sampleStat()}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err = UnmarshalResponse(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Has&HasStat == 0 || got.Stat == nil {
		t.Errorf("Has should have been set, got %08b stat=%v", got.Has, got.Stat)
	}
}

// A frame that stops in the middle of any field must produce an error, never a
// panic. The root-side server decodes frames written by an unprivileged process,
// so a truncated frame is expected input rather than an internal bug.
func TestTruncationNeverPanics(t *testing.T) {
	reqFrame, err := sampleRequest().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	respFrame, err := (&Response{
		ID: 7, Errno: 2, Stat: sampleStat(),
		Entries: []DirEntry{{Name: "a", Type: 8, Stat: sampleStat()}, {Name: "bb", Type: 4}},
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name  string
		frame []byte
		fn    func([]byte) error
	}{
		{"request", reqFrame, func(b []byte) error { _, err := UnmarshalRequest(b); return err }},
		{"response", respFrame, func(b []byte) error { _, err := UnmarshalResponse(b); return err }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for n := 0; n < len(tt.frame); n++ {
				func() {
					defer func() {
						if p := recover(); p != nil {
							t.Fatalf("panic decoding %d-byte prefix: %v", n, p)
						}
					}()
					if err := tt.fn(tt.frame[:n]); err == nil {
						t.Errorf("%d-byte prefix decoded without error", n)
					}
				}()
			}
			// The full frame must still be fine.
			if err := tt.fn(tt.frame); err != nil {
				t.Errorf("full frame failed: %v", err)
			}
		})
	}
}

func TestRejects(t *testing.T) {
	good, err := sampleRequest().Marshal()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("trailing bytes", func(t *testing.T) {
		// Silently ignoring extra bytes would let a frame carry a second meaning
		// past a decoder that only reads the first.
		if _, err := UnmarshalRequest(append(append([]byte(nil), good...), 0)); !errors.Is(err, ErrTrailing) {
			t.Errorf("err = %v, want ErrTrailing", err)
		}
	})

	t.Run("unknown op", func(t *testing.T) {
		b := append([]byte(nil), good...)
		b[8] = 0xFE // the op byte, straight after the u64 id
		if _, err := UnmarshalRequest(b); !errors.Is(err, ErrBadOp) {
			t.Errorf("err = %v, want ErrBadOp", err)
		}
		b[8] = 0
		if _, err := UnmarshalRequest(b); !errors.Is(err, ErrBadOp) {
			t.Errorf("op 0 must be rejected, got %v", err)
		}
	})

	t.Run("oversize frame", func(t *testing.T) {
		if _, err := UnmarshalRequest(make([]byte, MaxFrame+1)); !errors.Is(err, ErrTooLarge) {
			t.Errorf("err = %v, want ErrTooLarge", err)
		}
		big := &Request{Op: OpOpen, Value: make([]byte, MaxFrame)}
		if _, err := big.Marshal(); !errors.Is(err, ErrTooLarge) {
			t.Errorf("encoding oversize: err = %v, want ErrTooLarge", err)
		}
	})

	t.Run("field longer than its length prefix allows", func(t *testing.T) {
		r := &Request{Op: OpOpen, Path: strings.Repeat("a", 70000)}
		if _, err := r.Marshal(); !errors.Is(err, ErrFieldTooLong) && !errors.Is(err, ErrTooLarge) {
			t.Errorf("err = %v, want ErrFieldTooLong or ErrTooLarge", err)
		}
	})
}

// A frame may declare far more entries than it carries. The decoder must reject
// it from what is left in the buffer rather than sizing an allocation from the
// declared count — otherwise an unprivileged peer picks the root-side server's
// allocation size.
// An empty directory is a listing of zero entries, which is not the same as a
// response carrying no listing. Collapsing the two would make "the directory is
// empty" indistinguishable from "this reply was not about a listing".
func TestEmptyListingKeepsItsBit(t *testing.T) {
	b, err := (&Response{ID: 9, Has: HasEntries}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalResponse(b)
	if err != nil {
		t.Fatalf("empty listing must decode: %v", err)
	}
	if got.Has&HasEntries == 0 {
		t.Errorf("HasEntries must survive an empty listing, got %08b", got.Has)
	}
	if len(got.Entries) != 0 {
		t.Errorf("entries = %d, want 0", len(got.Entries))
	}
}

// Undefined bits are refused in both directions. Both binaries ship together, so
// there is no version skew to tolerate, and a bit that decodes without meaning
// anything is a channel the other side never agreed to.
func TestUnknownFlagBitsRejected(t *testing.T) {
	if _, err := (&Response{ID: 1, Has: 1 << 7}).Marshal(); !errors.Is(err, ErrBadFlags) {
		t.Errorf("encoding: err = %v, want ErrBadFlags", err)
	}
	b := make([]byte, 0, 16)
	b = binary.BigEndian.AppendUint64(b, 1)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = append(b, 1<<5)
	if _, err := UnmarshalResponse(b); !errors.Is(err, ErrBadFlags) {
		t.Errorf("decoding: err = %v, want ErrBadFlags", err)
	}
}

// The per-entry stat-presence byte is strictly 0 or 1. Treating any non-zero
// value as "present" would let two different frames decode to the same value,
// which defeats checking a frame by re-encoding it.
func TestEntryStatPresenceByteIsStrict(t *testing.T) {
	good, err := (&Response{ID: 1, Entries: []DirEntry{{Name: "a", Type: 8}}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// ...id(8) errno(4) has(1) count(4) len(2) 'a' type(1) present(1)
	b := append([]byte(nil), good...)
	b[len(b)-1] = 2
	if _, err := UnmarshalResponse(b); !errors.Is(err, ErrBadFlags) {
		t.Errorf("err = %v, want ErrBadFlags", err)
	}
}

func TestEntryCountCannotDriveAllocation(t *testing.T) {
	for _, count := range []uint32{0xFFFFFFFF, 1 << 20, 100} {
		b := make([]byte, 0, 32)
		b = binary.BigEndian.AppendUint64(b, 1) // id
		b = binary.BigEndian.AppendUint32(b, 0) // errno
		b = append(b, HasEntries)
		b = binary.BigEndian.AppendUint32(b, count)
		// ...and then nothing.
		if _, err := UnmarshalResponse(b); err == nil {
			t.Errorf("count %d with no entries decoded without error", count)
		}
	}
}

func FuzzUnmarshalRequest(f *testing.F) {
	if b, err := sampleRequest().Marshal(); err == nil {
		f.Add(b)
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := UnmarshalRequest(b)
		if err != nil {
			return
		}
		// Anything that decodes must re-encode to the identical bytes: the
		// decoder must not accept a frame it could not have produced.
		out, err := r.Marshal()
		if err != nil {
			t.Fatalf("decoded frame failed to re-encode: %v", err)
		}
		if !bytes.Equal(out, b) {
			t.Fatalf("re-encode differs:\n in %x\nout %x", b, out)
		}
	})
}

func FuzzUnmarshalResponse(f *testing.F) {
	if b, err := (&Response{ID: 3, Errno: 0, Has: HasFD, Stat: sampleStat()}).Marshal(); err == nil {
		f.Add(b)
	}
	if b, err := (&Response{ID: 4, Entries: []DirEntry{{Name: "x", Type: 8}}}).Marshal(); err == nil {
		f.Add(b)
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := UnmarshalResponse(b)
		if err != nil {
			return
		}
		out, err := r.Marshal()
		if err != nil {
			t.Fatalf("decoded frame failed to re-encode: %v", err)
		}
		if !bytes.Equal(out, b) {
			t.Fatalf("re-encode differs:\n in %x\nout %x", b, out)
		}
	})
}

// A GETXATTR reply carries a byte value, and an EMPTY value is distinct from no
// value at all — the HasValue bit, not the length, is what says one is present.
func TestResponseValueRoundTrip(t *testing.T) {
	for _, v := range [][]byte{{0x02, 0, 0, 0, 1, 2, 3, 4}, {}, {0xFF}} {
		want := &Response{ID: 9, Has: HasValue, Value: v}
		b, err := want.Marshal()
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		got, err := UnmarshalResponse(b)
		if err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got.Has&HasValue == 0 {
			t.Errorf("HasValue lost for %v", v)
		}
		if !bytes.Equal(got.Value, v) {
			t.Errorf("value = %v; want %v", got.Value, v)
		}
	}
}
