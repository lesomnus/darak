// Package wire is the message format spoken between the server and a user's
// helper process. See docs/helper-protocol.md.
//
// It is written by hand and depends on nothing. This code sits on the security
// boundary: the server runs as root and decodes frames produced by a process
// running as an ordinary user, so a decoder bug here is a root-side bug driven
// by unprivileged input. Fewer moving parts than a generated codec plus its
// runtime is the point, not an aesthetic preference.
//
// The transport is a SOCK_SEQPACKET socketpair, so one message is one frame and
// there is no length prefix to get wrong. File descriptors travel out of band as
// SCM_RIGHTS on the same sendmsg, never inside the payload.
package wire

// MaxFrame bounds a single message. Requests are a couple of paths; only a
// directory listing can approach it, and that has a resume rule (see Response's
// HasMore). It also caps how much a peer can make the other side allocate.
const MaxFrame = 64 << 10

// Op is the operation a request asks for.
//
// Every operation that touches the namespace has to be here rather than being
// something the server does for itself with a passed dirfd: openat/renameat/
// unlinkat/mkdirat re-check permission against the CALLING process, which is
// root, so the check would simply not happen. Passing an fd carries exactly one
// permission decision — read or write on that open file — and nothing else.
type Op uint8

const (
	OpOpen Op = iota + 1
	OpMkdir
	OpUnlink
	OpRmdir
	OpRename
	OpLink
	OpStat
	OpChmod
	OpSetXattr
	OpGetXattr
	OpReadDir

	opMax = OpReadDir
)

var opNames = [...]string{
	OpOpen:     "OPEN",
	OpMkdir:    "MKDIR",
	OpUnlink:   "UNLINK",
	OpRmdir:    "RMDIR",
	OpRename:   "RENAME",
	OpLink:     "LINK",
	OpStat:     "STAT",
	OpChmod:    "CHMOD",
	OpSetXattr: "SETXATTR",
	OpGetXattr: "GETXATTR",
	OpReadDir:  "READDIR",
}

func (o Op) String() string {
	if int(o) < len(opNames) && opNames[o] != "" {
		return opNames[o]
	}
	return "Op(" + itoa(uint64(o)) + ")"
}

// Valid reports whether o is a known operation. Decoding rejects anything else
// rather than letting an unknown op reach a handler switch.
func (o Op) Valid() bool { return o >= OpOpen && o <= opMax }

// Request flag bits. They are op-specific; each is documented where it applies.
const (
	// FlagNoFollow makes STAT and CHMOD act on a symlink itself rather than its
	// target.
	FlagNoFollow uint32 = 1 << 0
	// FlagWithStat asks READDIR to stat each entry. The listing UI needs size and
	// mtime per row, and doing it inside the helper keeps the server from calling
	// fstatat itself — which would be a path-based call, and so a check the
	// kernel would run against root instead of the user.
	FlagWithStat uint32 = 1 << 1
)

// Request is one operation. It is a single flat message for every op: a control
// protocol between two binaries built together does not earn a per-op type, and
// one shape means one encoder to audit.
type Request struct {
	ID    uint64
	Op    Op
	Flags uint32
	Mode  uint32
	Path  string
	Path2 string // RENAME and LINK destination
	Name  string // xattr name; READDIR resume cursor
	Value []byte // xattr value
}

// Stat is the subset of struct stat the server has any use for.
type Stat struct {
	Mode      uint32 // st_mode, including the file type bits
	UID       uint32
	GID       uint32
	Size      int64
	MtimeSec  int64
	MtimeNsec uint32
	Ino       uint64
	Nlink     uint64
}

// DirEntry is one READDIR result. Stat is set only when FlagWithStat was asked
// for and the entry could actually be stat'd.
type DirEntry struct {
	Name string
	Type uint8 // DT_* as reported by getdents
	Stat *Stat
}

// Response payload bits.
const (
	// HasFD means a file descriptor rides along as SCM_RIGHTS on this frame.
	HasFD uint8 = 1 << 0
	// HasStat means a Stat follows.
	HasStat uint8 = 1 << 1
	// HasEntries means a directory listing section follows. It is NOT the same as
	// "there are entries": an empty directory is a listing of zero entries, and
	// the server has to be able to tell that apart from a response that carries
	// no listing at all.
	HasEntries uint8 = 1 << 2
	// HasMore means the listing was truncated to fit MaxFrame. Resume by asking
	// again with Request.Name set to the last entry returned.
	HasMore uint8 = 1 << 3
	// HasValue means a byte string follows — the value read by GETXATTR. It is
	// distinct from "the value is empty": an xattr can hold zero bytes, and the
	// absence of the xattr is reported as an errno (ENODATA), not as this bit
	// being clear with a zero-length value.
	HasValue uint8 = 1 << 4

	// hasKnown is every defined bit. Undefined bits are refused in both
	// directions rather than ignored: the two binaries are built together, so
	// there is no version skew to be tolerant of, and a bit that survives
	// decoding without meaning anything is a place for a peer to carry state the
	// other side never agreed to.
	hasKnown = HasFD | HasStat | HasEntries | HasMore | HasValue
)

// Response is the reply to one Request, matched by ID. Responses may arrive in
// any order: the helper handles requests concurrently so that one large upload
// cannot block the same user's directory listing behind it.
type Response struct {
	ID  uint64
	Has uint8

	// Errno is a raw errno, zero on success. It is passed through untranslated.
	// Whether a path was denied or absent is the kernel's judgement; re-deriving
	// it in application code is how a second, disagreeing permission model gets
	// built by accident.
	Errno uint32

	Stat    *Stat
	Entries []DirEntry

	// Value is the byte string returned by GETXATTR, present only when HasValue
	// is set. It rides the same field space as a directory listing never does,
	// so the two are mutually exclusive in practice.
	Value []byte
}

// OK reports whether the operation succeeded.
func (r *Response) OK() bool { return r.Errno == 0 }

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
