// Package control is darak's client to the control plane: the service that owns
// the roster and changes it when told to. darak is the gate — it decides WHO may
// do a thing (an admin, a team's owner) — and this is how it hands the doing to
// the sink behind it once it has. There is no database here; the roster remains
// the single source of truth, and darak keeps none of it.
//
// The contract is proto/darak/control/v1, generated into controlpb. Nothing here
// wraps the generated clients in an interface of its own: a Controller is just
// the generated resource clients side by side, so a caller writes
// `ctrl.Enrollment.Add(ctx, req)` against exactly the types grpc produced. The
// resources — Enrollment, Membership — and their standard methods (Add, Get,
// List, Erase) are the whole vocabulary; there is no verb-per-operation seam to
// keep in step with them.
package control

import (
	"google.golang.org/grpc"

	"github.com/lesomnus/darak/control/controlpb"
)

// Controller is the set of resource clients darak calls the control plane
// through. Build it with Dial (a gRPC sidecar that commits the roster to a
// repository) or Local (edits a roster.yaml on this host) — either way darak
// calls the same generated interfaces, so where the roster lives is decided once
// here, not at every call site.
//
// The two service fields are independently optional. A deployment may have one
// backend serve Membership but not Enrollment: Local does exactly that (team
// changes edit the file; onboarding falls back to the approval queue), so a
// caller checks the specific field it needs, not just the Controller.
type Controller struct {
	// Enrollment is the onboarding lifecycle: Add on an unmapped identity's first
	// sign-in, Get to report how far it got. Nil when the deployment does not
	// auto-provision (Local leaves it nil) — the caller then uses the queue.
	Enrollment controlpb.EnrollmentServiceClient
	// Membership is the operator page's group changes: Add to put an account in a
	// group, Erase to take it out, Batch to land several at once.
	Membership controlpb.MembershipServiceClient

	conn *grpc.ClientConn
}

// Dial opens a Controller against a control-plane server at addr. The transport
// credentials are the caller's to choose: a sidecar on the loopback address
// wants insecure, anything over a network wants TLS. grpc.NewClient does not
// connect here — the first RPC does — so a control plane that is briefly down at
// startup does not stop darak from starting.
func Dial(addr string, opts ...grpc.DialOption) (*Controller, error) {
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, err
	}
	return &Controller{
		Enrollment: controlpb.NewEnrollmentServiceClient(conn),
		Membership: controlpb.NewMembershipServiceClient(conn),
		conn:       conn,
	}, nil
}

// Close releases the connection.
func (c *Controller) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
