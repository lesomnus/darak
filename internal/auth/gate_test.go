package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/lesomnus/darak/internal/helperpool"
)

type fakeResolver struct {
	known map[string]bool
}

func (f fakeResolver) Resolve(_ context.Context, user string) (helperpool.Creds, error) {
	if !f.known[user] {
		return helperpool.Creds{}, errors.New("no such user")
	}
	return helperpool.Creds{UID: 3001, GID: 3001}, nil
}

const pdbeditOut = "Unix username:\talice\nAccount Flags:\t[U          ]\n\n" +
	"Unix username:\terin\nAccount Flags:\t[DU         ]\n\n" +
	"Unix username:\tnoflags\n"

// The whole point of the gate: an identity provider says who somebody is and
// knows nothing about whether this server still has an account for them. These
// are the cases where it does not.
func TestMaySignIn(t *testing.T) {
	for name, tt := range map[string]struct {
		user       string
		runnerErr  error
		wantAllow  bool
		wantReason string
		wantErr    bool
	}{
		"active account":  {user: "alice", wantAllow: true},
		"suspended":       {user: "erin", wantReason: "account is suspended"},
		"no smb account":  {user: "dave", wantReason: "no SMB account"},
		"purged from nss": {user: "ghost", wantReason: "no such account on this server"},
		// A record with no flags line is still an account.
		"no flags line": {user: "noflags", wantAllow: true},
		// Not an account name at all — refused before anything is executed.
		"impossible name": {user: "-rf", wantReason: "not a possible account name"},
		// A passdb that cannot be asked is not permission to assume anything, and
		// it must not read as "this person is suspended" either.
		"passdb down": {user: "alice", runnerErr: errors.New("winbindd not running"), wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			g := Gate{
				Resolver: fakeResolver{known: map[string]bool{
					"alice": true, "erin": true, "dave": true, "noflags": true,
				}},
				Runner: &fakeRunner{out: pdbeditOut, err: tt.runnerErr},
			}

			got, err := g.MaySignIn(context.Background(), tt.user)
			if tt.wantErr {
				if !errors.Is(err, ErrUnavailable) {
					t.Fatalf("err = %v; want ErrUnavailable", err)
				}
				if got.Allowed {
					t.Error("an unanswerable question was reported as permission")
				}
				return
			}
			if err != nil {
				t.Fatalf("MaySignIn: %v", err)
			}
			if got.Allowed != tt.wantAllow {
				t.Errorf("Allowed = %v; want %v", got.Allowed, tt.wantAllow)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q; want %q", got.Reason, tt.wantReason)
			}
		})
	}
}

func TestParseAccountFlags(t *testing.T) {
	// The spacing differs between Samba versions, so both are exercised.
	got := ParseAccountFlags("Unix username:        alice\nAccount Flags:        [U          ]\n\n" +
		"Unix username:\tbob\nAccount Flags:\t[DU]\n")
	if !got["alice"] {
		t.Error("alice should be enabled")
	}
	if got["bob"] {
		t.Error("bob carries D and should be disabled")
	}
	if _, ok := got["carol"]; ok {
		t.Error("carol has no record and should be absent, not disabled")
	}
}
