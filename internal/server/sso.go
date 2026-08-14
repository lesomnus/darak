package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/lesomnus/darak/internal/auth"
	"github.com/lesomnus/darak/internal/identity"
	"github.com/lesomnus/darak/internal/provision"
	"github.com/lesomnus/darak/internal/sso"
)

// flowCookie names the in-flight sign-in. It holds an opaque id and nothing
// else: the state, the nonce and the PKCE verifier stay on the server.
const flowCookie = "darak_sso"

// Single sign-on, and what it is allowed to decide.
//
// The provider says who somebody is. It does not say whether they have an
// account here, and it is never asked: this handler resolves the assertion to a
// name through internal/identity and then puts that name through the same gate
// the password path goes through. `status: disabled` in roster.yaml still closes
// SMB, the password login and this, in one line — which is the property ADR-2
// refused to give up, and the reason SSO is acceptable now in a shape it was
// not before.
//
// Resolution order, and why:
//
//  1. The provider's subject. Immutable, so once it is pinned it is what
//     authenticates. An address that has been reassigned to a new hire resolves
//     to nothing here, rather than to whoever used to hold it.
//  2. An approved address, which also pins the subject for next time. This is
//     the only window in which an address is trusted on its own, and it is one
//     sign-in wide.
//  3. Nothing — the identity is recorded for an operator to decide about, and
//     the person is told to use their password in the meantime.

func (s *Server) handleSSOLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SSO == nil {
		http.NotFound(w, r)
		return
	}

	id, flow, err := s.flows.Begin(r.URL.Query().Get("return"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not start the sign-in")
		return
	}
	target, err := s.cfg.SSO.AuthCodeURL(r.Context(), flow)
	if err != nil {
		// The provider is unreachable. Saying so plainly is what lets somebody
		// decide to use their password instead of retrying for ten minutes.
		s.ssoFailure(w, r, "지금 SSO 제공자에 연결할 수 없습니다. 비밀번호로 로그인해 주세요.", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     flowCookie,
		Value:    id,
		Path:     "/api/sso/",
		MaxAge:   int(sso.FlowTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		// Lax, not Strict: the provider sends the browser back with a top-level
		// GET, and Strict would withhold the cookie on exactly that navigation —
		// every sign-in would fail with a missing flow.
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SSO == nil {
		http.NotFound(w, r)
		return
	}
	// However this ends, the flow is over.
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie, Value: "", Path: "/api/sso/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})

	c, err := r.Cookie(flowCookie)
	if err != nil {
		s.ssoFailure(w, r, "로그인 요청을 확인할 수 없습니다. 처음부터 다시 시도해 주세요.", errors.New("no flow cookie"))
		return
	}
	flow, ok := s.flows.Take(c.Value)
	if !ok {
		s.ssoFailure(w, r, "로그인 요청이 만료되었습니다. 다시 시도해 주세요.", errors.New("unknown or expired flow"))
		return
	}

	q := r.URL.Query()
	if desc := q.Get("error"); desc != "" {
		s.ssoFailure(w, r, "SSO 제공자가 로그인을 거부했습니다.", errors.New(desc))
		return
	}
	// The state must be the one this browser started with. It is compared
	// against server-side state rather than against another cookie, so a
	// planted cookie has nothing to agree with.
	if q.Get("state") != flow.State {
		s.ssoFailure(w, r, "로그인 요청이 일치하지 않습니다. 다시 시도해 주세요.", errors.New("state mismatch"))
		return
	}

	ident, err := s.cfg.SSO.Exchange(r.Context(), q.Get("code"), flow)
	switch {
	case errors.Is(err, sso.ErrUnavailable):
		s.ssoFailure(w, r, "지금 SSO 제공자에 연결할 수 없습니다. 비밀번호로 로그인해 주세요.", err)
		return
	case errors.Is(err, sso.ErrWrongTenant):
		s.ssoFailure(w, r, "이 조직의 계정이 아닙니다.", err)
		return
	case errors.Is(err, sso.ErrNoAddress):
		s.ssoFailure(w, r, "이 계정에는 이 서버가 인정하는 주소가 없습니다. 관리자에게 문의해 주세요.", err)
		return
	case err != nil:
		s.ssoFailure(w, r, "로그인을 확인하지 못했습니다.", err)
		return
	}

	account, err := s.resolveIdentity(r.Context(), ident)
	if err != nil {
		s.ssoFailure(w, r, "로그인을 처리하지 못했습니다. 관리자에게 문의해 주세요.", err)
		return
	}
	if account == "" {
		s.queueIdentity(w, r, ident)
		return
	}

	// The same gate the password path passes through, asked on every sign-in
	// rather than once at approval: an approval is not a standing permission,
	// and twelve hours is a long time to be wrong about somebody's account.
	verdict, err := s.cfg.Gate.MaySignIn(r.Context(), account)
	if err != nil {
		s.ssoFailure(w, r, "지금 계정 상태를 확인할 수 없습니다. 잠시 후 다시 시도해 주세요.", err)
		return
	}
	if !verdict.Allowed {
		// The person is told one thing and the log is told another. Which of
		// "no such account" and "suspended" applies is a fact about an account,
		// and the browser asking is not yet known to own it.
		slog.Warn("sso sign-in refused by the account gate",
			"account", account, "reason", verdict.Reason, "subject", ident.Subject)
		s.ssoFailure(w, r, "이 계정으로는 지금 로그인할 수 없습니다. 관리자에게 문의해 주세요.", errors.New(verdict.Reason))
		return
	}

	token, err := s.sessions.Create(account)
	if err != nil {
		s.ssoFailure(w, r, "세션을 시작하지 못했습니다.", err)
		return
	}
	setCookie(w, token, s.cfg.SessionTTL, s.cfg.SecureCookies)
	slog.Info("sso sign-in", "account", account, "issuer", ident.Issuer)
	http.Redirect(w, r, flow.Return, http.StatusSeeOther)
}

// resolveIdentity turns a verified assertion into an account name, or "" when
// nothing here answers for it.
//
// An error means the mapping could not be updated — the answer is known but
// could not be recorded — which is deliberately not treated as "no account".
// Signing somebody in while failing to pin their subject would leave the
// address trusted on its own indefinitely, which is the one state this design
// tries not to be in.
func (s *Server) resolveIdentity(ctx context.Context, ident *sso.Identity) (string, error) {
	// 1. The subject, if it has been seen before.
	if account, ok := s.cfg.Identities.BySubject(ident.Issuer, ident.Subject); ok {
		// They arrived with an address this mapping does not list yet — an alias,
		// or a new address after a rename. The subject already proved who they
		// are, so this is recorded rather than queued: an approval an operator
		// could only rubber-stamp is not a decision, it is a chore.
		for _, e := range ident.Emails {
			if owner, known := s.cfg.Identities.ByEmail(e); known {
				if owner != account {
					// Their subject says one account and an address they carry is
					// approved for another. Nothing is changed — the subject wins,
					// as it does everywhere else — but somebody should look at it.
					slog.Warn("an SSO identity carries an address approved for another account",
						"account", account, "address", e, "other", owner)
				}
				continue
			}
			if err := s.cfg.Identities.AttachEmail(account, e, time.Now()); err != nil {
				slog.Warn("could not record a new address for an approved identity",
					"account", account, "err", err)
				continue
			}
			s.journal(identity.JournalEntry{
				Action: "attach", Account: account,
				Issuer: ident.Issuer, Subject: ident.Subject, Emails: []string{e},
			})
			slog.Info("recorded a new address for an approved identity", "account", account, "address", e)
		}
		return account, nil
	}

	// 2. An approved address, which pins the subject for next time.
	for _, e := range ident.Emails {
		account, ok := s.cfg.Identities.ByEmail(e)
		if !ok {
			continue
		}
		if err := s.cfg.Identities.Pin(account, ident.Issuer, ident.Subject, time.Now()); err != nil {
			if errors.Is(err, identity.ErrSubjectPinned) || errors.Is(err, identity.ErrTaken) {
				// Somebody else's directory object is presenting an address that
				// answers for this account. That is what pinning exists to catch:
				// most likely the address was reassigned after the original holder
				// left. It is refused and reported, never resolved.
				slog.Warn("sso identity conflicts with a pinned subject",
					"account", account, "address", e, "subject", ident.Subject)
				return "", nil
			}
			return "", err
		}
		s.journal(identity.JournalEntry{
			Action: "pin", Account: account,
			Issuer: ident.Issuer, Subject: ident.Subject, Emails: []string{e},
		})
		slog.Info("pinned an SSO identity on first sign-in",
			"account", account, "address", e, "issuer", ident.Issuer)
		return account, nil
	}

	// 3. Nobody yet. Provisioning may be able to change that.
	return s.provision(ctx, ident), nil
}

// provision asks the configured endpoint to create an account, and returns the
// name only if one now exists that did not before.
//
// This is the only path in the system where access appears without a person
// acting, so what it will accept is narrow: a rule has to match, the endpoint
// has to report having CREATED an account (not merely that one exists), the
// name has to be one that did not resolve when it answered, and the gate has to
// then admit it — which means the roster reconcile has actually run and tdbsam
// has the account enabled. Anything short of all four ends in the queue with a
// human deciding, exactly as before.
func (s *Server) provision(ctx context.Context, ident *sso.Identity) string {
	if s.cfg.Provision == nil {
		return ""
	}
	req := provision.Request{
		Issuer:  ident.Issuer,
		Subject: ident.Subject,
		Emails:  ident.Emails,
		Name:    ident.Name,
	}
	out := s.cfg.Provision.Run(ctx, req, ident.Claims)

	switch out.Kind {
	case provision.NoRule:
		return ""
	case provision.Denied:
		slog.Info("provisioning declined an identity", "rule", out.Rule, "subject", ident.Subject)
		return ""
	case provision.Unavailable:
		slog.Warn("provisioning could not be completed", "rule", out.Rule, "err", out.Err,
			"subject", ident.Subject)
		return ""
	case provision.Accepted:
		// The endpoint took it but is not finished — a pull request against the
		// roster, most likely. Nothing to wait for, and nothing for an operator to
		// do here either; the next sign-in will find the account.
		slog.Info("provisioning accepted an identity for later", "rule", out.Rule,
			"subject", ident.Subject)
		return ""
	case provision.Existing:
		// The endpoint named an account that already exists. It did not create it,
		// whatever it said, so this is a person to be looked at rather than let
		// in: it is either a re-registration, or a hook pointing at somebody
		// else's account.
		slog.Warn("provisioning named an account that already exists; sending it to the queue instead",
			"rule", out.Rule, "account", out.Account, "subject", ident.Subject)
		return ""
	case provision.Created:
	default:
		return ""
	}

	if !s.cfg.Provision.Await(ctx, out.Account) {
		// The account was created but the system has not caught up. Not an error
		// and not something to act on — the reconcile is running, and the next
		// attempt finds it.
		slog.Info("provisioned an account that has not converged yet",
			"rule", out.Rule, "account", out.Account)
		return ""
	}

	// Bind, with the same call an administrator's approval makes. Approve
	// refuses if the account already answers to a different identity, so the
	// last word still belongs to the store rather than to the endpoint.
	if _, err := s.cfg.Identities.Approve(
		out.Account, ident.Issuer, ident.Subject, ident.Emails,
		"provision:"+out.Rule, time.Now(),
	); err != nil {
		slog.Warn("could not bind a provisioned identity", "account", out.Account, "err", err)
		return ""
	}
	s.journal(identity.JournalEntry{
		Action: "provision", Account: out.Account,
		Issuer: ident.Issuer, Subject: ident.Subject, Emails: ident.Emails,
	})
	slog.Info("provisioned and bound an identity", "rule", out.Rule, "account", out.Account,
		"subject", ident.Subject)
	return out.Account
}

// queueIdentity records an unmapped identity and tells the person what happens
// next.
//
// Nothing is created and nothing is granted — this is a request, and it is kept
// in a store that has no lookup path into the sign-in decision at all.
func (s *Server) queueIdentity(w http.ResponseWriter, r *http.Request, ident *sso.Identity) {
	req := identity.Request{
		Issuer:  ident.Issuer,
		Subject: ident.Subject,
		Emails:  ident.Emails,
		Name:    ident.Name,
	}
	if err := s.cfg.Pending.Record(req, time.Now()); err != nil {
		slog.Warn("could not record an SSO access request", "subject", ident.Subject, "err", err)
	} else {
		slog.Info("sso access requested", "subject", ident.Subject, "addresses", ident.Emails)
	}

	addr := ""
	if len(ident.Emails) > 0 {
		addr = ident.Emails[0]
	}
	s.notice(w, r, notice{
		Kind:    "pending",
		Message: "접근 요청이 기록되었습니다. 관리자가 승인하면 SSO로 로그인할 수 있습니다.",
		Address: addr,
	})
}

// SweepSSO drops abandoned sign-ins, spent notices and expired access requests.
//
// All three are self-limiting already — a flow and a notice are single-use and
// checked against their expiry when taken, and the queue sweeps on write — so
// this is about not holding memory for a system that has gone quiet. The queue
// is the one with a reason beyond that: it is the only store an unauthenticated
// caller can grow, and nothing else would ever prune a request that stopped
// being retried.
func (s *Server) SweepSSO() {
	if s.cfg.SSO == nil {
		return
	}
	s.flows.Sweep()
	s.notices.mu.Lock()
	s.notices.sweep(time.Now())
	s.notices.mu.Unlock()
	if err := s.cfg.Pending.Sweep(); err != nil {
		slog.Warn("could not persist the access request queue", "err", err)
	}
}

// --- telling the person what happened ---

// notice is a one-off message for the page the browser lands on.
//
// It is fetched by id rather than carried in the URL. A message in a query
// parameter is text an attacker can choose, rendered by this server, on the page
// that asks for a password — which is a phishing surface for the sake of saving
// a map.
type notice struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Address string `json:"address,omitempty"`

	expires time.Time
}

// noticeTTL is short: this is read by the redirect that immediately follows.
const noticeTTL = 5 * time.Minute

type notices struct {
	mu sync.Mutex
	m  map[string]notice
}

func newNotices() *notices { return &notices{m: map[string]notice{}} }

func (n *notices) put(id string, v notice) {
	v.expires = time.Now().Add(noticeTTL)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sweep(time.Now())
	n.m[id] = v
}

// take reads a notice once. Single use, so a shared or bookmarked URL shows
// nothing.
func (n *notices) take(id string) (notice, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	v, ok := n.m[id]
	if !ok {
		return notice{}, false
	}
	delete(n.m, id)
	if time.Now().After(v.expires) {
		return notice{}, false
	}
	return v, true
}

func (n *notices) sweep(now time.Time) {
	for id, v := range n.m {
		if now.After(v.expires) {
			delete(n.m, id)
		}
	}
}

// ssoFailure logs the real reason and shows the person a usable one.
//
// The two are different on purpose. What went wrong is often a fact about
// somebody's account or about the provider, and the browser asking has not
// proved it owns either.
func (s *Server) ssoFailure(w http.ResponseWriter, r *http.Request, message string, cause error) {
	slog.Warn("sso sign-in failed", "err", cause, "remote", r.RemoteAddr)
	s.notice(w, r, notice{Kind: "error", Message: message})
}

func (s *Server) notice(w http.ResponseWriter, r *http.Request, v notice) {
	id, err := newToken()
	if err != nil {
		// Without an id there is nowhere to put the message. The login page is
		// still the right destination; it will simply say nothing.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.notices.put(id, v)
	http.Redirect(w, r, "/?sso="+url.QueryEscape(id), http.StatusSeeOther)
}

// handleSSONotice serves a message left by the callback. Unauthenticated,
// because the person reading it is by definition not signed in, and single-use,
// so the id in the URL is worth nothing once the page has loaded.
func (s *Server) handleSSONotice(w http.ResponseWriter, r *http.Request) {
	v, ok := s.notices.take(r.URL.Query().Get("id"))
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"kind": ""})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}

// --- admin surface ---

// ssoOnly answers 404 when no provider is configured, matching what adminOnly
// does for a non-admin: a deployment without the feature is indistinguishable
// from a build without it.
func (s *Server) ssoOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.SSO == nil {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

// handleIdentityJournal serves the history of who bound what to whom.
func (s *Server) handleIdentityJournal(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 1000 {
			n = parsed
		}
	}
	entries, err := s.cfg.Journal.Tail(n)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "기록을 읽을 수 없습니다: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// handleIdentityList reports the approved mappings, together with the roster
// disagreements. See identity.Store.Check for why those are reported rather
// than enforced.
func (s *Server) handleIdentityList(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"mappings": s.cfg.Identities.List(),
		"pending":  s.cfg.Pending.List(),
		"problems": []identity.Problem{},
	}
	if d, err := s.cfg.Admin.Declaration(r.Context()); err == nil {
		status := map[string]string{}
		for _, u := range d.Users {
			status[u.Name] = u.Status
		}
		out["problems"] = s.cfg.Identities.Check(status)
	} else {
		out["warning"] = "roster를 읽을 수 없어 매핑을 대조하지 못했습니다: " + err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// handleProvisioning reports the auto-provisioning rules in force.
//
// Read-only, and there is no route that writes them. The rules are a deployed
// file precisely because they can grant an account without anybody clicking
// anything — an endpoint an administrator could point somewhere else from a web
// page would be a way to grant themselves one. What this answers is the
// question that leaves: "which version of that file is actually running", which
// somebody will ask the first time an edit does not seem to have taken.
func (s *Server) handleProvisioning(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ProvisionConfig == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	st := s.cfg.ProvisionConfig()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true,
		"status":  st,
	})
}

// handleIdentityApprove binds a queued request to an account.
//
// POST /api/admin/identities  {"account":"...","issuer":"...","subject":"..."}
func (s *Server) handleIdentityApprove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Account string `json:"account"`
		Issuer  string `json:"issuer"`
		Subject string `json:"subject"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	// Only an account the roster knows. Without this the queue would be a way to
	// aim a mapping at any name Samba happens to have, including a service
	// account.
	if err := s.cfg.Admin.Managed(r.Context(), body.Account); err != nil {
		writeError(w, http.StatusBadRequest, "관리 대상 계정이 아닙니다: "+body.Account)
		return
	}
	req, ok := s.cfg.Pending.Get(body.Issuer, body.Subject)
	if !ok {
		writeError(w, http.StatusNotFound, "그 요청은 이미 없습니다")
		return
	}

	m, err := s.cfg.Identities.Approve(
		body.Account, req.Issuer, req.Subject, req.Emails, userOf(r), time.Now())
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, identity.ErrSubjectPinned) || errors.Is(err, identity.ErrTaken) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	if err := s.cfg.Pending.Discard(req.Issuer, req.Subject); err != nil {
		slog.Warn("approved an identity but could not clear the request", "err", err)
	}
	s.journal(identity.JournalEntry{
		By: userOf(r), Action: "approve", Account: body.Account,
		Issuer: req.Issuer, Subject: req.Subject, Emails: req.Emails,
	})
	slog.Info("sso identity approved", "by", userOf(r), "account", body.Account, "subject", req.Subject)
	writeJSON(w, http.StatusOK, m)
}

// handleIdentityDiscard drops a queued request without approving it.
//
// DELETE /api/admin/identities/pending?issuer=&subject=
func (s *Server) handleIdentityDiscard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := s.cfg.Pending.Discard(q.Get("issuer"), q.Get("subject")); err != nil {
		writeError(w, http.StatusNotFound, "그 요청은 이미 없습니다")
		return
	}
	s.journal(identity.JournalEntry{
		By: userOf(r), Action: "discard", Issuer: q.Get("issuer"), Subject: q.Get("subject"),
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleIdentityForget removes a mapping.
//
// DELETE /api/admin/identities/<account>
//
// This is for a mapping that is wrong, not for a person who has left: removing
// it closes no session and stops no SMB access. Offboarding is `status:
// disabled` in the roster, which closes everything at once.
func (s *Server) handleIdentityForget(w http.ResponseWriter, r *http.Request) {
	account := requestPath(r, "/api/admin/identities/")
	if err := s.cfg.Identities.Forget(account); err != nil {
		writeError(w, http.StatusNotFound, "그런 매핑이 없습니다")
		return
	}
	s.journal(identity.JournalEntry{By: userOf(r), Action: "forget", Account: account})
	w.WriteHeader(http.StatusNoContent)
}

// journal records a change to who may sign in as whom.
//
// Keeping the mapping out of git costs the answer `git blame` used to give —
// who bound this address to this account, and when — because a file the
// application rewrites simply loses its previous contents. Every line of it is
// a statement about who may sign in as whom, so the history has to exist
// somewhere. A failure to write it is logged and dropped: an audit trail that
// can refuse an approval is an audit trail that can take the feature down.
func (s *Server) journal(e identity.JournalEntry) {
	if s.cfg.Journal == nil {
		return
	}
	if err := s.cfg.Journal.Append(e); err != nil {
		slog.Warn("could not record an identity change", "err", err, "account", e.Account)
	}
}

// AccountGate is what the SSO path asks before issuing a session. It is
// satisfied by auth.Gate.
type AccountGate interface {
	MaySignIn(ctx context.Context, user string) (auth.Verdict, error)
}
