package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lesomnus/darak/internal/ui"
)

// runUI is `darak ui`: the interface, served from a directory, unprivileged.
//
// Two ways it runs. On its own, for a deployment that ships the interface
// separately, it listens on -addr and the API server is pointed at it with
// -ui-upstream. As the API server's child it is given the socket to serve on as
// -listen-fd, and never picks an address.
//
// It does not authenticate anybody and forwards nothing: the API server in front
// of it strips the session before a request gets here (internal/uiproc).
func runUI(args []string) error {
	fl := flag.NewFlagSet("darak ui", flag.ExitOnError)
	var (
		addr     = fl.String("addr", ":8080", "listen address; ignored with -listen-fd")
		dir      = fl.String("dir", "/usr/share/darak/ui", "the built interface (web/ after `npm run build`)")
		listenFD = fl.Int("listen-fd", -1, "serve on this inherited listening socket instead of -addr; set by the API server when it starts this as its child")
	)
	if err := fl.Parse(args); err != nil {
		return err
	}

	// Refuses root; see ui.Open.
	fsys, err := ui.Open(*dir)
	if err != nil {
		return err
	}

	var ln net.Listener
	if *listenFD >= 0 {
		f := os.NewFile(uintptr(*listenFD), "ui-listener")
		if f == nil {
			return fmt.Errorf("-listen-fd %d is not open", *listenFD)
		}
		ln, err = net.FileListener(f)
		f.Close()
	} else {
		ln, err = net.Listen("tcp", *addr)
	}
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	h := &http.Server{
		Handler:           ui.Handler(fsys),
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		slog.Info("serving the interface", "addr", ln.Addr().String(), "dir", *dir, "uid", os.Geteuid())
		if err := h.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.Shutdown(shutdownCtx)
	}
}
