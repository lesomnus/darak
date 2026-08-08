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
	"syscall"
	"time"

	"github.com/lesomnus/darak/internal/auth"
	"github.com/lesomnus/darak/internal/helperpool"
	"github.com/lesomnus/darak/internal/run"
	"github.com/lesomnus/darak/internal/server"
	"github.com/lesomnus/darak/internal/share"
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
		sessionTTL    = flag.Duration("session-ttl", 12*time.Hour, "how long a login lasts")
		secureCookies = flag.Bool("secure-cookies", true, "mark the session cookie Secure (turn off only for plain-HTTP development)")
		idleTimeout   = flag.Duration("helper-idle-timeout", helperpool.DefaultIdleTimeout, "how long an unused helper is kept")
		maxHelpers    = flag.Int("max-helpers", helperpool.DefaultMaxHelpers, "cap on concurrently running helpers")
		credsTTL      = flag.Duration("creds-ttl", helperpool.DefaultCredsTTL, "how long a credential lookup is reused; also the delay before a group change takes effect")
		maxUpload     = flag.Int64("max-upload", 64<<30, "largest accepted request body in bytes")
		sharesFile    = flag.String("shares", "/var/lib/darak/shares.json", "where share links are kept; must NOT be on the data volume")
		noUI          = flag.Bool("no-ui", false, "serve only the API")
	)
	flag.Parse()

	// Starting a process as another user needs privilege. Without it every
	// request would fail at its first file access, which is a confusing way to
	// discover a deployment mistake.
	if os.Geteuid() != 0 {
		return errors.New("must run as root: helpers are started as the requesting user")
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

	srv, err := server.New(server.Config{
		FS:            &vfs.FS{Pool: pool},
		Shares:        shares,
		UI:            web,
		Auth:          auth.NTLM{Runner: run.Exec{}, Path: *ntlmAuthBin},
		SessionTTL:    *sessionTTL,
		SecureCookies: *secureCookies,
		MaxUpload:     *maxUpload,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go housekeeping(ctx, pool, srv, shares)

	h := &http.Server{
		Addr:    *addr,
		Handler: srv.Handler(),
		// A stalled client must not hold a helper slot open indefinitely, but an
		// upload can legitimately take a long time — so bound the handshake and
		// the headers, and leave the body to MaxUpload and the client's patience.
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", *addr, "root", *root)
		if err := h.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
			if err := shares.Sweep(); err != nil {
				slog.Warn("could not persist the share store", "err", err)
			}
		}
	}
}
