package control

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/lesomnus/darak/control/controlpb"
	"github.com/lesomnus/darak/internal/run"
)

// Local builds a Controller that edits a roster.yaml on THIS host, for the
// common deployment that keeps the roster as a local writable file rather than
// behind a GitOps pipeline. It is the counterpart of Dial: where Dial hands the
// change to a sidecar that commits it to a repository, Local makes the change
// here with `usersync member` (which edits the file — validating it, preserving
// its comments, taking a lock) and then `usersync apply` to converge the running
// system. It is the same tool an operator would run by hand, so darak's web edit
// and a shell edit go the one way.
//
// Onboarding is deliberately left off: creating an account from an SSO sign-in
// is the one thing that happens without a human, and a local deployment has the
// operator right there, so an unmapped identity is better sent to the approval
// queue than silently written into the roster. Enrollment is therefore nil, and
// darak falls back to that queue — see the caller's guard.
func Local(runner run.Runner, usersyncBin string) *Controller {
	if usersyncBin == "" {
		usersyncBin = "usersync"
	}
	return &Controller{
		Membership: &localMembership{runner: runner, bin: usersyncBin},
	}
}

// localMembership implements controlpb.MembershipServiceClient against the local
// `usersync` binary. It satisfies the SAME generated client interface the gRPC
// client does, so darak calls it identically — the choice of where the roster
// lives is made once, when the Controller is built, not at every call site.
type localMembership struct {
	runner run.Runner
	bin    string
}

// Add puts an account in a group as a writing member and converges. Only the
// member role is roster-backed, so a reader/owner role is refused rather than
// silently dropped — the same contract the gRPC server keeps.
func (m *localMembership) Add(ctx context.Context, in *controlpb.AddMembershipRequest, _ ...grpc.CallOption) (*controlpb.Membership, error) {
	if r := in.GetRole(); r != controlpb.Role_ROLE_MEMBER && r != controlpb.Role_ROLE_UNSPECIFIED {
		return nil, status.Errorf(codes.Unimplemented, "role %s is not backed by the roster", r)
	}
	if err := m.member(ctx, "add", in.GetAccount(), in.GetGroup()); err != nil {
		return nil, err
	}
	if err := m.apply(ctx); err != nil {
		return nil, err
	}
	return &controlpb.Membership{Account: in.GetAccount(), Group: in.GetGroup(), Role: controlpb.Role_ROLE_MEMBER}, nil
}

// Erase takes an account out of a group and converges.
func (m *localMembership) Erase(ctx context.Context, in *controlpb.EraseMembershipRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	if err := m.member(ctx, "remove", in.GetAccount(), in.GetGroup()); err != nil {
		return nil, err
	}
	if err := m.apply(ctx); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// Batch edits several memberships and converges ONCE at the end rather than
// after each — the single-convergence the batch is for. A change with an
// unsupported role or op fails the batch before it converges; a `usersync
// member` edit that fails does too. There is no in-memory rollback of edits
// already made: the roster is the desired state, and the recovery is another
// apply, not putting the declaration back.
func (m *localMembership) Batch(ctx context.Context, in *controlpb.BatchMembershipsRequest, _ ...grpc.CallOption) (*controlpb.BatchMembershipsResponse, error) {
	applied := 0
	for _, c := range in.GetChanges() {
		switch c.GetOp() {
		case controlpb.MembershipChange_OP_ADD:
			if r := c.GetRole(); r != controlpb.Role_ROLE_MEMBER && r != controlpb.Role_ROLE_UNSPECIFIED {
				return nil, status.Errorf(codes.Unimplemented, "role %s is not backed by the roster", r)
			}
			if err := m.member(ctx, "add", c.GetAccount(), c.GetGroup()); err != nil {
				return nil, err
			}
			applied++
		case controlpb.MembershipChange_OP_ERASE:
			if err := m.member(ctx, "remove", c.GetAccount(), c.GetGroup()); err != nil {
				return nil, err
			}
			applied++
		default:
			return nil, status.Error(codes.InvalidArgument, "each change needs op ADD or ERASE")
		}
	}
	if applied > 0 {
		if err := m.apply(ctx); err != nil {
			return nil, err
		}
	}
	return &controlpb.BatchMembershipsResponse{Applied: int32(applied)}, nil
}

// List and Grade are not offered locally: List has no caller, and Grade (a
// re-grade to reader/owner) is not roster-backed here any more than the member
// role is. Returning Unimplemented keeps parity with the gRPC server.
func (m *localMembership) List(context.Context, *controlpb.ListMembershipsRequest, ...grpc.CallOption) (*controlpb.ListMembershipsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "list is not implemented by the local controller")
}

func (m *localMembership) Grade(context.Context, *controlpb.GradeMembershipRequest, ...grpc.CallOption) (*controlpb.Membership, error) {
	return nil, status.Error(codes.Unimplemented, "grade is not implemented by the local controller")
}

func (m *localMembership) member(ctx context.Context, op, account, group string) error {
	if _, err := m.runner.Run(ctx, "", m.bin, "member", op, account, group); err != nil {
		return status.Errorf(codes.Unavailable, "usersync member %s %s %s: %v", op, account, group, err)
	}
	return nil
}

func (m *localMembership) apply(ctx context.Context) error {
	if _, err := m.runner.Run(ctx, "", m.bin, "apply"); err != nil {
		return status.Errorf(codes.Unavailable, "the roster was updated but not converged (usersync apply): %v", err)
	}
	return nil
}
