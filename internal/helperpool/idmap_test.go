package helperpool

import (
	"errors"
	"strings"
	"testing"
)

// A shifted user namespace is the failure this catches, and it is a quiet one:
// the helper starts as uid 3001, the file it writes is recorded as something
// else entirely, and nothing goes wrong until somebody cannot open their own
// files — or the wrong person can.
func TestCheckMapping(t *testing.T) {
	for name, tt := range map[string]struct {
		lines   []string
		wantErr bool
		// A phrase the message must carry, so the operator is told what to do
		// rather than only that something is off.
		wantHint string
	}{
		"initial namespace": {
			lines: []string{"         0          0 4294967295"},
		},
		"identity, plenty of room": {
			lines: []string{"0 0 1000000"},
		},
		"docker userns-remap": {
			lines:    []string{"         0     231072      65536"},
			wantErr:  true,
			wantHint: "userns-remap",
		},
		"rootless podman": {
			lines:    []string{"0 1000 1", "1 100000 65536"},
			wantErr:  true,
			wantHint: "on disk",
		},
		// Identity as far as it goes, but the managed band starts at 3000 and
		// runs to 19999; a short map leaves those unmapped.
		"identity but truncated": {
			lines:    []string{"0 0 1000"},
			wantErr:  true,
			wantHint: "managed band",
		},
		// An unconfigured namespace maps nothing, and every uid becomes the
		// overflow id — files owned by nobody.
		"empty": {
			lines:    nil,
			wantErr:  true,
			wantHint: "empty",
		},
		"unparseable": {
			lines:    []string{"who knows"},
			wantErr:  true,
			wantHint: "shape",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkMapping("/proc/self/uid_map", tt.lines)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error")
			}
			if !errors.Is(err, ErrRemappedIdentity) {
				t.Errorf("error should be ErrRemappedIdentity so a caller can offer an override: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("message should mention %q, got: %v", tt.wantHint, err)
			}
		})
	}
}

// Whatever this machine is, the real check must agree with itself: it either
// passes, or it explains itself.
func TestCheckIdentityMappingOnThisMachine(t *testing.T) {
	if err := CheckIdentityMapping(); err != nil {
		if !errors.Is(err, ErrRemappedIdentity) {
			t.Fatalf("unexpected error kind: %v", err)
		}
		t.Logf("this machine is remapped, which the check reports: %v", err)
	}
}
