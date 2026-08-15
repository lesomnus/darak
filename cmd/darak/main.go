// Command darak serves the file server's web API.
//
// It runs privileged, because it has to start a helper as each requesting user,
// and it does no file access of its own: every path is resolved by that helper,
// with that user's credentials. See docs/helper-protocol.md and nas-design.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lesomnus/darak/internal/activity"
	"github.com/lesomnus/darak/internal/admin"
	"github.com/lesomnus/darak/internal/auth"
	"github.com/lesomnus/darak/internal/helperpool"
	"github.com/lesomnus/darak/internal/identity"
	"github.com/lesomnus/darak/internal/provision"
	"github.com/lesomnus/darak/internal/run"
	"github.com/lesomnus/darak/internal/server"
	"github.com/lesomnus/darak/internal/share"
	"github.com/lesomnus/darak/internal/sso"
	"github.com/lesomnus/darak/internal/ui"
	"github.com/lesomnus/darak/internal/vfs"
)

func main() {
	if err := realMain(); err != nil {
		slog.Error("darak", "err", err)
		os.Exit(1)
	}
}

func realMain() error {
	var (
		addr          = flag.String("addr", ":8080", "listen address")
		root          = flag.String("root", "/srv/data", "the data tree to serve")
		helperBin     = flag.String("helper", "darak-helper", "path to the helper binary")
		ntlmAuthBin   = flag.String("ntlm-auth", "ntlm_auth", "path to ntlm_auth")
		smbpasswdBin  = flag.String("smbpasswd", "smbpasswd", "path to smbpasswd, used when somebody changes their own password")
		sessionTTL    = flag.Duration("session-ttl", 12*time.Hour, "how long a login lasts")
		secureCookies = flag.Bool("secure-cookies", true, "mark the session cookie Secure (turn off only for plain-HTTP development)")
		idleTimeout   = flag.Duration("helper-idle-timeout", helperpool.DefaultIdleTimeout, "how long an unused helper is kept")
		maxHelpers    = flag.Int("max-helpers", helperpool.DefaultMaxHelpers, "cap on concurrently running helpers")
		credsTTL      = flag.Duration("creds-ttl", helperpool.DefaultCredsTTL, "how long a credential lookup is reused; also the delay before a group change takes effect")
		maxUpload     = flag.Int64("max-upload", 64<<30, "largest accepted request body in bytes")
		sharesFile    = flag.String("shares", "/var/lib/darak/shares.json", "where share links are kept; must NOT be on the data volume")
		noUI          = flag.Bool("no-ui", false, "serve only the API")
		tlsCert       = flag.String("tls-cert", "", "PEM certificate chain; serves HTTPS when set together with -tls-key")
		tlsKey        = flag.String("tls-key", "", "PEM private key")
		allowRemapped = flag.Bool("allow-remapped-uids", false, "start even though uids here are not the numbers on disk (see the error this suppresses)")
		adminGroup    = flag.String("admin-group", admin.DefaultGroup, "POSIX group whose members may use the operator page; empty disables it")
		anonymousUser = flag.String("anonymous-user", "", "OS account to serve unauthenticated web requests as, for public (roster `anonymous:`) folders; must exist in NSS, be in no group, and have no SMB credential. Empty disables anonymous access entirely")
		usageInterval = flag.Duration("usage-interval", 30*time.Minute, "how often per-user disk usage is remeasured")
		activityDir   = flag.String("activity", "/var/lib/darak/activity", "where the who-changed-what record is kept; empty disables it")
		activityKeep  = flag.Duration("activity-keep", activity.DefaultKeep, "how long to keep activity records (permanent retention is a backup's job)")
		smbLog        = flag.String("smb-log", "/var/log/samba/audit.log", "smbd's log, read for full_audit records; empty disables the SMB half")
		brandName     = flag.String("brand-name", server.DefaultBrandName, "what the interface calls this installation, in the corner and in the page title")
		brandLogo     = flag.String("brand-logo", "", "image file to draw in the corner instead of the built-in mark (svg, png, jpg, webp, gif, avif, ico; read once at startup)")

		oidcIssuer     = flag.String("oidc-issuer", "", "OpenID Connect issuer URL; enables signing in with the company account (everything else here is ignored when empty)")
		oidcClientID   = flag.String("oidc-client-id", "", "the application registered with the provider")
		oidcSecretFile = flag.String("oidc-client-secret-file", "", "file holding the client secret; a file rather than a flag because argv is world-readable through /proc")
		oidcRedirect   = flag.String("oidc-redirect-url", "", "the callback URL registered with the provider, e.g. https://darak.example.com/api/sso/callback")
		oidcTenant     = flag.String("oidc-tenant", "", "required `tid` claim; mandatory for the multi-tenant Microsoft endpoints, where the issuer names no directory")
		oidcDomains    = flag.String("oidc-email-domains", "", "comma-separated address domains to accept; empty accepts every address the tenant asserts")
		oidcForward    = flag.Bool("sso-forward-auth", false, "trust a reverse proxy (oauth2-proxy behind Traefik ForwardAuth) that authenticates and hands the verified id_token on the Authorization header; darak still verifies it. No client secret or redirect URL is used; -oidc-client-id is the audience the token must carry (the proxy's client id)")
		ssoTrustEmail  = flag.Bool("sso-trust-email", false, "bind an EXISTING account to an SSO identity on first sign-in without operator approval, when a trusted-domain address derives that account's name. Removes the approval step for members the roster already has; new identities still go through provisioning/approval. Requires -oidc-email-domains (refused without it)")
		identitiesFile = flag.String("identities", "/var/lib/darak/identities.json", "where approved identity mappings are kept; must NOT be on the data volume")
		pendingFile    = flag.String("identity-requests", "/var/lib/darak/identity-requests.json", "where unapproved sign-in requests are queued")
		journalFile    = flag.String("identity-journal", "/var/lib/darak/identity-journal.jsonl", "append-only record of every mapping change; empty disables it")

		provisionFile = flag.String("provision-config", "", "rules for asking an external service to create an account for an SSO identity nobody has approved; empty means every unmapped identity waits for an administrator")
		provisionPoll = flag.Duration("provision-reload", provision.DefaultPollInterval, "how often the provisioning rules are re-read; a broken file keeps the last good one")
	)
	flag.Parse()

	// Starting a process as another user needs privilege. Without it every
	// request would fail at its first file access, which is a confusing way to
	// discover a deployment mistake.
	if os.Geteuid() != 0 {
		return errors.New("must run as root: helpers are started as the requesting user")
	}
	// A shifted user namespace makes every uid mean a different number on disk,
	// so files would be written owned by somebody the roster does not name — and
	// nothing would fail at the time. It surfaces much later, as files their
	// owner cannot open.
	if err := helperpool.CheckIdentityMapping(); err != nil {
		if !*allowRemapped {
			return err
		}
		slog.Warn("starting with remapped uids because -allow-remapped-uids was given", "err", err)
	}

	// Resolve the helper up front for the same reason. This is an operator-
	// supplied executable, not anything from a request — no path from a user is
	// ever resolved in this process.
	binPath, err := exec.LookPath(*helperBin)
	if err != nil {
		return fmt.Errorf("helper binary %q: %w", *helperBin, err)
	}

	pool, err := helperpool.New(helperpool.Config{
		Bin:         binPath,
		Root:        *root,
		IdleTimeout: *idleTimeout,
		MaxHelpers:  *maxHelpers,
		CredsTTL:    *credsTTL,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Share links live outside the served tree on purpose: nas-design.md §7
	// requires the data volume to stay free of application state, because that
	// volume is what later becomes a shared filesystem several gateways mount.
	shares, err := share.NewFileStore(*sharesFile)
	if err != nil {
		return err
	}

	var web http.Handler
	if !*noUI {
		web = ui.Handler()
	}

	// Read now rather than per request: this process resolves no path of its own
	// once it is serving, and an operator who mistyped -brand-logo should find
	// out here instead of from a broken image.
	brand, err := server.LoadBrand(*brandName, *brandLogo)
	if err != nil {
		return err
	}

	// The operator surface is optional and off by nothing but an empty flag:
	// with no admin group there is no page, and every route behind it answers
	// exactly as it does for a signed-in user who is not an admin.
	var adm *admin.Admin
	if *adminGroup != "" {
		adm, err = admin.New(admin.Config{
			Group: *adminGroup,
			Root:  *root,
		})
		if err != nil {
			return err
		}
	}

	// The who-changed-what record. Two sources, because there are two ways in
	// and each already knows the AUTHENTICATED identity: smbd's full_audit for
	// SMB (including a mounted share, which is still SMB underneath), and
	// darak's own handlers for the web, which run as the session's user.
	//
	// There is no kernel-level option here worth reaching for: the audit
	// subsystem is not namespaced and a container cannot register with it, and
	// fanotify needs CAP_SYS_ADMIN and reports a pid rather than a name.
	var acts *activity.Store
	if *activityDir != "" {
		acts, err = activity.NewStore(*activityDir, *activityKeep)
		if err != nil {
			return err
		}
		defer acts.Close()
	}

	// Single sign-on, which is off unless an issuer is given.
	//
	// It adds a way to prove WHO somebody is. It adds no way to be allowed
	// anything: the account behind an assertion is looked up here, and whether
	// that account may sign in at all is still answered by the same tdbsam the
	// password path asks (nas-design.md ADR-2). So `status: disabled` in the
	// roster continues to close SMB, the password login and this one together.
	var (
		ssoProvider *sso.Provider
		identities  *identity.Store
		pending     *identity.Queue
		journal     *identity.Journal
		gate        server.AccountGate
		provisioner *provision.Provisioner
		watcher     *provision.Watcher
	)
	if *oidcIssuer != "" {
		// Trust-email binds an account whenever a token carries an address whose
		// local part names it. That is only safe if the addresses are already
		// confined to domains this deployment controls — otherwise anyone with an
		// address anywhere whose local part matches a member's name would be let
		// straight in. Refuse the combination rather than run an open door.
		if *ssoTrustEmail && *oidcDomains == "" {
			return errors.New("-sso-trust-email requires -oidc-email-domains: trusting an address to name an account is only safe within domains you control")
		}
		secret := ""
		if *oidcSecretFile != "" {
			secret, err = sso.ReadSecret(*oidcSecretFile)
			if err != nil {
				return err
			}
		}
		ssoProvider, err = sso.New(sso.Config{
			Issuer:       *oidcIssuer,
			ClientID:     *oidcClientID,
			ClientSecret: secret,
			RedirectURL:  *oidcRedirect,
			Tenant:       *oidcTenant,
			Domains:      splitList(*oidcDomains),
			ForwardAuth:  *oidcForward,
		})
		if err != nil {
			return err
		}
		// A malformed mapping file is fatal; see identity.NewFileStore. A
		// malformed request queue is not, because it grants nothing and letting
		// unreviewed input decide whether the server starts would be the worse
		// failure.
		identities, err = identity.NewFileStore(*identitiesFile)
		if err != nil {
			return err
		}
		pending, err = identity.NewFileQueue(*pendingFile)
		if err != nil {
			slog.Warn("starting with an empty access request queue", "err", err)
		}
		if *journalFile != "" {
			journal, err = identity.NewJournal(*journalFile)
			if err != nil {
				return err
			}
		}
		theGate := auth.Gate{
			Resolver:   helperpool.NSSResolver{},
			Runner:     run.Exec{},
			PdbeditBin: "pdbedit",
		}
		gate = theGate

		// Auto-provisioning. Off unless a rules file is named, and it is a FILE
		// rather than anything editable from the page on purpose: this is the one
		// path where an account can appear without an administrator acting, so
		// what it may do has to be a reviewed, deployed artifact.
		//
		// A file that will not load is fatal here, unlike a reload: the operator
		// asked for this by naming it, and coming up with it silently inert would
		// send everybody to the approval queue with nothing saying why.
		if *provisionFile != "" {
			watcher, err = provision.NewWatcher(*provisionFile, *provisionPoll)
			if err != nil {
				return err
			}
			provisioner = &provision.Provisioner{
				Config: watcher.Config,
				Gate:   theGate,
			}
			slog.Info("auto-provisioning enabled", "config", *provisionFile,
				"rules", len(watcher.Config().Rules))
		}
		// Discovery now rather than on the first click, so a mistyped issuer is
		// an error in the log at boot. It is a warning and not a failure: the
		// password path has to keep working, and it is needed most when the
		// provider is the thing that is down.
		warmCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := ssoProvider.Warm(warmCtx)
		cancel()
		if err != nil {
			slog.Warn("the identity provider could not be reached; SSO will retry, "+
				"and password sign-in is unaffected", "err", err)
		}
	}

	fs := &vfs.FS{Pool: pool}
	if acts != nil {
		fs.Record = func(user, action, p, to string) {
			e := activity.Event{
				User: user, Action: activity.Action(action),
				Path: p, To: to, Source: activity.SourceWeb,
			}
			// A failure to write the note must not fail the operation that
			// already succeeded -- so it is logged, loudly, and dropped.
			if err := acts.Record(e); err != nil {
				slog.Warn("could not record activity", "err", err, "user", user, "path", p)
			}
		}
	}

	srv, err := server.New(server.Config{
		FS:             fs,
		Activity:       acts,
		Shares:         shares,
		Admin:          adm,
		UI:             web,
		Brand:          brand,
		Auth:           auth.NTLM{Runner: run.Exec{}, Path: *ntlmAuthBin},
		Passwords:      &auth.PasswordStore{Runner: run.Exec{}, Path: *smbpasswdBin},
		SSO:            ssoProvider,
		SSOForwardAuth: *oidcForward,
		Identities:     identities,
		Pending:        pending,
		Journal:        journal,
		Gate:           gate,
		TrustEmail:     *ssoTrustEmail,
		Provision:      provisioner,
		ProvisionConfig: func() provision.Status {
			if watcher == nil {
				return provision.Status{}
			}
			return watcher.Status()
		},
		SessionTTL:    *sessionTTL,
		SecureCookies: *secureCookies,
		MaxUpload:     *maxUpload,
		AnonymousUser: *anonymousUser,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go housekeeping(ctx, pool, srv, shares)
	if watcher != nil {
		// The rules reload without a restart, for the reason the roster does: a
		// restart drops every session, and a cost like that is what makes people
		// stop making changes through the mechanism that was built for them.
		go watcher.Run(ctx.Done(), func(err error) {
			slog.Warn("provisioning rules could not be reloaded; the last good ones are still in force",
				"err", err)
		})
	}
	if acts != nil && *smbLog != "" {
		go acts.Tail(ctx.Done(), *smbLog, *root, func(err error) {
			slog.Warn("could not record an SMB activity event", "err", err)
		})
	}
	if adm != nil {
		go measureUsage(ctx, adm, *usageInterval)
	}

	h := &http.Server{
		Addr:    *addr,
		Handler: srv.Handler(),
		// A stalled client must not hold a helper slot open indefinitely, but an
		// upload can legitimately take a long time — so bound the handshake and
		// the headers, and leave the body to MaxUpload and the client's patience.
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// Half a TLS configuration is a deployment that silently serves plain HTTP
	// while everyone believes otherwise.
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("-tls-cert and -tls-key must be given together")
	}
	serveTLS := *tlsCert != ""
	if !serveTLS && *secureCookies {
		// Secure cookies are never sent over plain HTTP, so the session would be
		// issued and then never come back — a login that appears to work and does
		// nothing. Say so rather than letting it be debugged from scratch.
		slog.Warn("serving plain HTTP with -secure-cookies=true; put TLS in front of this, " +
			"or logins will succeed and no session will ever be sent back")
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", *addr, "root", *root, "tls", serveTLS)
		var err error
		if serveTLS {
			err = h.ListenAndServeTLS(*tlsCert, *tlsKey)
		} else {
			err = h.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return h.Shutdown(shutdownCtx)
	}
}

// splitList reads a comma-separated flag, dropping empties so a trailing comma
// does not become an entry nothing can ever match.
func splitList(s string) []string {
	out := []string{}
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// housekeeping trims what would otherwise only grow.
//
// Both are also self-limiting on their own — the pool reaps before starting a
// helper, and an expired session never resolves — so this is about not holding
// processes and memory for a system that has gone quiet, not about correctness.
func housekeeping(ctx context.Context, pool *helperpool.Pool, srv *server.Server, shares *share.Store) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pool.Reap()
			srv.Sessions().Sweep()
			srv.SweepSSO()
			if err := shares.Sweep(); err != nil {
				slog.Warn("could not persist the share store", "err", err)
			}
		}
	}
}

// measureUsage keeps the per-user usage report fresh.
//
// It runs here rather than inside the handler because measuring is the one
// operator query whose cost scales with the size of the data instead of the
// number of accounts. A request serves whatever the last pass produced, tagged
// with when that was, so the page stays responsive on a full and slow disk --
// which is precisely when someone opens it.
//
// The first pass runs immediately so the page is not empty for the first
// interval after a restart.
func measureUsage(ctx context.Context, adm *admin.Admin, every time.Duration) {
	for {
		adm.RefreshUsage(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
