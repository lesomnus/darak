//darak:local-state
//
// This file resolves a fixed path, which the rest of the server must never do.
// The exemption is the same shape as the share store's: /proc/self/uid_map is
// not in the served tree, it names this process rather than anything a request
// asked for, and there is no user whose helper could open it — it is how the
// process asks about itself.
//
// See internal/lint for the check this marker is read by.

package helperpool

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrRemappedIdentity means uids in this process are not the numbers the
// filesystem records.
var ErrRemappedIdentity = errors.New("helperpool: uids are remapped")

// uidMapPath is where the kernel reports this process's user-namespace mapping.
const uidMapPath = "/proc/self/uid_map"

// CheckIdentityMapping verifies that a uid here is the same number on disk.
//
// The entire design rests on a uid meaning one thing: the helper runs as uid N,
// the file it creates is owned by N, and the roster says N is that person. In a
// user namespace with a shifted map that stops being true — a helper started as
// 3001 writes a file the host records as 234073, and the ownership of everything
// it touches is quietly wrong. Nothing fails at the time. It surfaces later, as
// files nobody can open, or as the wrong person opening them.
//
// Docker does not remap by default, but `userns-remap` in daemon.json turns it
// on globally and is exactly the sort of thing enabled for unrelated reasons on
// a shared host. Checking costs one file read at startup.
func CheckIdentityMapping() error {
	f, err := os.Open(uidMapPath)
	if err != nil {
		// No /proc, or a kernel without user namespaces. There is nothing to
		// verify and nothing to report — refusing to start here would break more
		// than it protects.
		return nil
	}
	defer f.Close()
	return checkMapping(f.Name(), readLines(f))
}

func readLines(f *os.File) []string {
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// checkMapping is the parsing, split out so it can be tested against the shapes
// the kernel actually writes rather than only the one this machine has.
//
// Each line is "<id inside> <id outside> <count>". Identity means every id maps
// to itself, which in practice is the single line "0 0 4294967295" the initial
// namespace reports.
func checkMapping(path string, lines []string) error {
	seen := false
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			// A line the kernel wrote that this does not understand means the
			// format is not what it assumes. Skipping it and concluding "identity"
			// from the rest would be the wrong way to be wrong.
			return fmt.Errorf("%w: %s is not in a shape this understands: %q", ErrRemappedIdentity, path, strings.TrimSpace(line))
		}
		inside, err1 := strconv.ParseUint(fields[0], 10, 32)
		outside, err2 := strconv.ParseUint(fields[1], 10, 32)
		count, err3 := strconv.ParseUint(fields[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			return fmt.Errorf("%w: %s is not in a shape this understands: %q", ErrRemappedIdentity, path, line)
		}
		seen = true
		if inside != outside {
			return fmt.Errorf(
				"%w: uid %d here is uid %d on disk (%s says %q). Every file this server "+
					"writes would be owned by a number the roster does not name, and nothing "+
					"would fail at the time. Turn off userns-remap, or pass "+
					"-allow-remapped-uids if the dataset really is shifted to match",
				ErrRemappedIdentity, inside, outside, path, strings.TrimSpace(line))
		}
		// A mapping that starts at identity but is too short still shifts the ids
		// above it, and the managed band starts at 3000.
		if count < 1<<16 {
			return fmt.Errorf(
				"%w: %s maps only %d ids from %d (%q), so ids above that are unmapped or "+
					"shifted; the managed band runs into the tens of thousands",
				ErrRemappedIdentity, path, count, inside, strings.TrimSpace(line))
		}
	}
	if !seen {
		// An empty map means the namespace has not been configured yet, and every
		// uid resolves to the overflow id. Writing anything in that state produces
		// files owned by nobody.
		return fmt.Errorf("%w: %s is empty", ErrRemappedIdentity, path)
	}
	return nil
}
