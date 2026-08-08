package activity

import (
	"strings"
	"time"
)

// Reading smbd's full_audit output.
//
// The module is configured (see deploy/*/entrypoint.sh) with
//
//	full_audit:prefix = %u|%I|%S
//
// so a line is  user|clientIP|share|op|result|...  with the remaining fields
// depending on the operation. Samba writes it into its own log, where the
// payload sits on the line AFTER a header:
//
//	[2026/08/08 12:21:13.698792,  1] .../vfs_full_audit.c:637(do_log)
//	  alice|127.0.0.1|team-a|unlinkat|ok|/srv/data/teams/team-a/final.txt
//
// Three things about that output are not obvious and all three were found by
// running it rather than by reading about it:
//
//  1. An INVALID OPERATION NAME DOES NOT DISABLE AUDITING, IT BREAKS THE SHARE.
//     `full_audit:success = mkdir` makes smb_full_audit_connect fail the
//     connect outright. The names in Samba 4.22 are the *at() forms —
//     mkdirat, unlinkat, renameat — not mkdir/unlink/rename. Hence
//     ValidateOpNames, run at startup against the binary that will serve.
//
//  2. Samba CREATES A DIRECTORY UNDER A TEMPORARY NAME and renames it:
//     mkdirat on `.::TMPNAME:D:4707%1755…:sub`, then renameat to `sub`. A
//     reader that trusts the mkdirat reports a path nobody will recognise, so
//     the mkdirat is dropped and the rename that follows is what becomes the
//     mkdir event.
//
//  3. create_file FIRES ON PLAIN OPENS TOO, including opening the share's own
//     root directory. Only the `create` and `overwrite_if` dispositions mean
//     something was made.

// auditMarker identifies a payload line. Matching on the prefix format rather
// than on the header keeps this working if Samba's log framing changes.
const auditFields = 5 // user|ip|share|op|result

// tmpNamePrefix is what Samba names a directory before renaming it into place.
const tmpNamePrefix = ".::TMPNAME:"

// OpNames are the full_audit operations this parser understands, and therefore
// exactly what the deployment should ask smbd to log. Asking for more is noise;
// asking for fewer loses events.
//
// Kept here rather than in the entrypoint so the producer and the consumer
// cannot drift apart: ValidateOpNames checks these against the running smbd.
var OpNames = []string{"create_file", "mkdirat", "unlinkat", "renameat"}

// ParseAuditLine turns one full_audit payload into an event.
//
// ok is false for a line that is not an audit payload, is an operation this
// does not report on, or is one of the noise cases above. That is the common
// outcome and not an error: most of what smbd logs is not a change.
//
// root is the served tree, stripped so a path reads the way the interface
// addresses it. A path outside root is dropped rather than reported with an
// absolute path that would leak the container's layout.
func ParseAuditLine(line string, root string, at time.Time) (Event, bool) {
	i := strings.Index(line, "|")
	if i < 0 {
		return Event{}, false
	}
	f := strings.Split(strings.TrimSpace(line), "|")
	if len(f) < auditFields {
		return Event{}, false
	}
	user, from, op, result := strings.TrimSpace(f[0]), f[1], f[3], f[4]
	if user == "" || result != "ok" {
		// A failed operation changed nothing. Recording attempts is a different
		// feature (and a much noisier one) than recording changes.
		return Event{}, false
	}

	e := Event{At: at, User: user, From: from, Source: SourceSMB}

	switch op {
	case "unlinkat":
		p, ok := rel(root, last(f, 5))
		if !ok {
			return Event{}, false
		}
		e.Action, e.Path = Deleted, p

	case "renameat":
		// ...|renameat|ok|<from>|<to>
		if len(f) < 7 {
			return Event{}, false
		}
		src, dst := f[len(f)-2], f[len(f)-1]
		to, ok := rel(root, dst)
		if !ok {
			return Event{}, false
		}
		// A rename OUT OF a temp name is Samba finishing a mkdir (case 2).
		if strings.Contains(base(src), tmpNamePrefix) {
			e.Action, e.Path = Mkdir, to
			return e, true
		}
		from, ok := rel(root, src)
		if !ok {
			return Event{}, false
		}
		e.Action, e.Path, e.To = Renamed, from, to

	case "mkdirat":
		// Dropped when it is the temp-name half; the rename above reports it.
		p, ok := rel(root, last(f, 5))
		if !ok || strings.Contains(base(p), tmpNamePrefix) {
			return Event{}, false
		}
		e.Action, e.Path = Mkdir, p

	case "create_file":
		// ...|create_file|ok|<access>|<type>|<disposition>|<path>
		if len(f) < 8 {
			return Event{}, false
		}
		disposition, path := f[len(f)-2], f[len(f)-1]
		switch disposition {
		case "create", "overwrite_if", "overwrite", "supersede":
		default:
			return Event{}, false // a plain open (case 3)
		}
		if f[len(f)-3] == "dir" {
			// The directory was already reported by the rename out of TMPNAME;
			// reporting it again would double every mkdir.
			return Event{}, false
		}
		p, ok := rel(root, path)
		if !ok {
			return Event{}, false
		}
		e.Action, e.Path = Created, p

	default:
		return Event{}, false
	}
	return e, true
}

// last returns the final field, or "" when the line is too short.
func last(f []string, min int) string {
	if len(f) < min {
		return ""
	}
	return f[len(f)-1]
}

func base(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// rel makes an absolute path relative to the served root.
//
// A path outside root is refused rather than reported: the audit view addresses
// files the way the rest of the interface does, and an absolute path from
// inside the container is both meaningless to a reader and a small disclosure.
func rel(root, p string) (string, bool) {
	if p == "" {
		return "", false
	}
	root = strings.TrimSuffix(root, "/")
	switch {
	case p == root:
		return "", false
	case strings.HasPrefix(p, root+"/"):
		return strings.TrimPrefix(p, root+"/"), true
	default:
		return "", false
	}
}

// ExtractPayload pulls the audit payload out of a samba log line pair.
//
// Samba writes a header line and then the message, indented. A reader that
// looks only for the pipe format handles both that and a syslog line, which is
// why this does not try to parse the header.
func ExtractPayload(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if strings.HasPrefix(s, "[20") || strings.Contains(s, "vfs_full_audit.c") {
		return "", false
	}
	if strings.Count(s, "|") < auditFields-1 {
		return "", false
	}
	return s, true
}
