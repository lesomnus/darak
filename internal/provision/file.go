//darak:local-state
//
// This file resolves two operator-given paths: the rules, and the bearer token
// a rule sends. Both come from configuration rather than from a request, and
// neither is a file any user owns.
//
// See internal/lint for the check this marker is read by.

package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// readSecret loads a bearer token.
//
// A file for the same reason the OIDC client secret is one, and the reasoning
// is written out in internal/sso/secret.go: argv is world-readable through
// /proc, and helpers are exec'd from this process and inherit its environment.
func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("provision: bearer token: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("provision: bearer token file %s is empty", path)
	}
	return token, nil
}

// Status is what the operator page shows about the configuration in force.
//
// It exists because this file decides who gets an account without anybody
// clicking anything, and "which version of it is actually running" is then a
// question somebody will need answered — especially after an edit that was
// supposed to take effect. It never contains a token, only the path one was
// read from.
type Status struct {
	Path string `json:"path"`
	// LoadedAt is when the configuration currently in force was accepted.
	LoadedAt time.Time `json:"loaded_at,omitzero"`
	// Digest identifies the accepted bytes, so an operator can tell at a glance
	// whether what is running is what they committed.
	Digest string `json:"digest,omitempty"`

	Timeout string `json:"timeout,omitempty"`
	Wait    string `json:"wait,omitempty"`
	Rules   []Rule `json:"rules"`

	// Error is set when the file on disk right now cannot be used. The rules
	// above are then the LAST GOOD ones, which are still in force — that is the
	// whole point of reporting both.
	Error string `json:"error,omitempty"`
	// ErrorAt is when that failure was noticed.
	ErrorAt time.Time `json:"error_at,omitzero"`
}

// Watcher keeps the configuration up to date without a restart.
//
// It polls rather than watching for events. The file is a few hundred bytes, a
// read every few seconds costs nothing, and polling handles the one case an
// inotify watch pinned to a path gets wrong: a Kubernetes ConfigMap update does
// not rewrite the file, it swaps a symlink underneath it. Reading through the
// path and hashing the bytes notices that and an in-place edit alike.
type Watcher struct {
	path     string
	interval time.Duration

	mu      sync.RWMutex
	cfg     *Config
	status  Status
	digest  string
	stopped chan struct{}
	once    sync.Once
}

// DefaultPollInterval is how often the file is re-read. An account being
// provisioned a few seconds later than an edit intended is not a cost anybody
// can perceive.
const DefaultPollInterval = 5 * time.Second

// NewWatcher loads the configuration once and returns a watcher for it.
//
// A file that cannot be read or parsed at startup is a hard error: the operator
// asked for this feature by naming the file, and starting with it silently
// inert would mean everybody landing in the approval queue with nothing saying
// why. That is the opposite of the reload behaviour, deliberately — at startup
// there is no last-good version to fall back to.
func NewWatcher(path string, interval time.Duration) (*Watcher, error) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	w := &Watcher{path: path, interval: interval, stopped: make(chan struct{})}
	w.status.Path = path

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("provision: %s: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("provision: %s: %w", path, err)
	}
	w.accept(cfg, data)
	return w, nil
}

// Config returns the configuration in force. Never nil once the watcher exists.
func (w *Watcher) Config() *Config {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.cfg
}

// Status reports what is in force and whether the file on disk disagrees.
func (w *Watcher) Status() Status {
	w.mu.RLock()
	defer w.mu.RUnlock()
	s := w.status
	s.Rules = append([]Rule(nil), w.status.Rules...)
	return s
}

// Run polls until ctx is done. Intended to be started in a goroutine.
func (w *Watcher) Run(done <-chan struct{}, onErr func(error)) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-w.stopped:
			return
		case <-t.C:
			if err := w.reload(); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}

// Stop ends a Run started without a done channel of its own.
func (w *Watcher) Stop() { w.once.Do(func() { close(w.stopped) }) }

// reload re-reads the file, replacing the configuration only if the new one is
// usable.
//
// A broken file leaves the running rules exactly as they were, and says so
// through Status. This is the same rule the roster's hot-reload follows
// (deploy/prod/entrypoint.sh): a bad edit must not change a running system, and
// it must not take one down either. Refusing to serve because somebody
// mistyped a domain would turn a typo into an outage; carrying on with the last
// good rules and showing the error turns it into a fix.
func (w *Watcher) reload() error {
	data, err := os.ReadFile(w.path)
	if err != nil {
		w.fail(fmt.Errorf("provision: %s: %w", w.path, err))
		return err
	}
	sum := digestOf(data)

	w.mu.RLock()
	unchanged := sum == w.digest
	w.mu.RUnlock()
	if unchanged {
		return nil
	}

	cfg, err := Parse(data)
	if err != nil {
		w.fail(err)
		return err
	}
	w.accept(cfg, data)
	return nil
}

func (w *Watcher) accept(cfg *Config, data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cfg = cfg
	w.digest = digestOf(data)
	w.status = Status{
		Path:     w.path,
		LoadedAt: time.Now(),
		Digest:   w.digest,
		Timeout:  cfg.Timeout.String(),
		Wait:     cfg.Wait.String(),
		Rules:    cfg.Rules,
	}
}

func (w *Watcher) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.Error = err.Error()
	w.status.ErrorAt = time.Now()
}

// digestOf is a short content hash, shown to operators rather than compared
// against anything secret — twelve hex characters is enough to tell two
// versions of a small file apart by eye.
func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}
