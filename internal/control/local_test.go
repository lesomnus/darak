package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lesomnus/darak/control/controlpb"
)

// recRunner records the commands it was asked to run and can be told to fail
// one of them.
type recRunner struct {
	calls []string
	fail  map[string]error
}

func (r *recRunner) Run(_ context.Context, _ /*stdin*/, name string, args ...string) (string, error) {
	key := strings.TrimSpace(name + " " + strings.Join(args, " "))
	r.calls = append(r.calls, key)
	if e, ok := r.fail[key]; ok {
		return "", e
	}
	return "", nil
}

func TestLocalMembershipAddErase(t *testing.T) {
	r := &recRunner{}
	c := Local(r, "usersync")
	ctx := context.Background()

	if _, err := c.Membership.Add(ctx, &controlpb.AddMembershipRequest{Account: "bob", Group: "team-a"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := c.Membership.Erase(ctx, &controlpb.EraseMembershipRequest{Account: "alice", Group: "team-a"}); err != nil {
		t.Fatalf("Erase: %v", err)
	}
	want := []string{
		"usersync member add bob team-a",
		"usersync apply",
		"usersync member remove alice team-a",
		"usersync apply",
	}
	if strings.Join(r.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v\nwant %v", r.calls, want)
	}
}

// A role the roster does not back is refused before anything is written.
func TestLocalMembershipRejectsUnbackedRole(t *testing.T) {
	r := &recRunner{}
	_, err := Local(r, "usersync").Membership.Add(context.Background(),
		&controlpb.AddMembershipRequest{Account: "bob", Group: "team-a", Role: controlpb.Role_ROLE_READER})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("Add with reader role = %v, want Unimplemented", err)
	}
	if len(r.calls) != 0 {
		t.Fatalf("a refused role still ran %v", r.calls)
	}
}

// A batch makes every member edit, then converges exactly once at the end.
func TestLocalMembershipBatchConvergesOnce(t *testing.T) {
	r := &recRunner{}
	resp, err := Local(r, "usersync").Membership.Batch(context.Background(), &controlpb.BatchMembershipsRequest{
		Changes: []*controlpb.MembershipChange{
			{Op: controlpb.MembershipChange_OP_ADD, Account: "a", Group: "x"},
			{Op: controlpb.MembershipChange_OP_ERASE, Account: "b", Group: "y"},
		},
	})
	if err != nil {
		t.Fatalf("Batch: %v", err)
	}
	if resp.GetApplied() != 2 {
		t.Fatalf("applied = %d, want 2", resp.GetApplied())
	}
	want := []string{
		"usersync member add a x",
		"usersync member remove b y",
		"usersync apply",
	}
	if strings.Join(r.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v\nwant %v", r.calls, want)
	}
}

// A failed member edit fails the batch and does not converge.
func TestLocalMembershipBatchStopsOnError(t *testing.T) {
	r := &recRunner{fail: map[string]error{"usersync member add a x": errors.New("locked")}}
	_, err := Local(r, "usersync").Membership.Batch(context.Background(), &controlpb.BatchMembershipsRequest{
		Changes: []*controlpb.MembershipChange{{Op: controlpb.MembershipChange_OP_ADD, Account: "a", Group: "x"}},
	})
	if err == nil {
		t.Fatal("Batch should fail when a member edit fails")
	}
	for _, c := range r.calls {
		if c == "usersync apply" {
			t.Error("a failed batch must not converge")
		}
	}
}

// Local does not offer onboarding: Enrollment is nil so the caller falls back to
// the approval queue.
func TestLocalHasNoEnrollment(t *testing.T) {
	if Local(&recRunner{}, "usersync").Enrollment != nil {
		t.Error("Local must leave Enrollment nil")
	}
}
