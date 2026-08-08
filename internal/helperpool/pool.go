package helperpool

import (
	"container/list"
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/lesomnus/darak/internal/wire"
)

// Defaults for Config.
const (
	DefaultIdleTimeout = 5 * time.Minute
	DefaultMaxHelpers  = 64
	DefaultCredsTTL    = 30 * time.Second
)

// Config tunes a Pool.
type Config struct {
	// Bin is the darak-helper binary; Root is the tree it serves.
	Bin  string
	Root string

	// IdleTimeout is how long an unused helper is kept. A helper is a process
	// and a few descriptors, so keeping one costs little and starting one costs
	// a fork+exec on the first request of every burst.
	IdleTimeout time.Duration

	// MaxHelpers caps concurrently running helpers. Reaching it evicts the least
	// recently used idle helper; if every helper is busy the request fails rather
	// than queueing behind an unrelated user. Set it above the expected number of
	// concurrent users.
	MaxHelpers int

	// CredsTTL is how long a credential lookup is reused before being refreshed.
	//
	// It is the delay between a team membership changing and the affected user
	// seeing it, so it trades staleness against an NSS round trip per request.
	// Group changes are rare and requests are not.
	CredsTTL time.Duration

	// Resolver looks up uid/gid/groups. Defaults to NSSResolver.
	Resolver Resolver

	// spawn is overridable so the pool's own tests can run without root.
	spawn func(Spec) (*Helper, error)
}

type entry struct {
	user   string
	helper *Helper

	// inUse is the number of requests currently in flight. A helper is never
	// stopped while this is above zero: doing so would fail an operation that
	// has already been permitted, for a reason unrelated to it.
	inUse int

	idleSince time.Time
	creds     Creds
	credsAt   time.Time
	elem      *list.Element // position in lru
}

// Pool keeps one helper per user.
//
// One per user is not an optimisation, it is the model: a helper IS a user's
// credentials, so two users cannot share one and one user gains nothing from
// two. What the pool adds is deciding when to start, replace, and stop them.
type Pool struct {
	cfg Config

	mu      sync.Mutex
	helpers map[string]*entry
	lru     *list.List // *entry, most recently used at the front
	closed  bool
}

// New builds a Pool. Bin and Root are required.
func New(cfg Config) (*Pool, error) {
	if cfg.Bin == "" || cfg.Root == "" {
		return nil, fmt.Errorf("helperpool: Bin and Root are required")
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.MaxHelpers <= 0 {
		cfg.MaxHelpers = DefaultMaxHelpers
	}
	if cfg.CredsTTL <= 0 {
		cfg.CredsTTL = DefaultCredsTTL
	}
	if cfg.Resolver == nil {
		cfg.Resolver = NSSResolver{}
	}
	if cfg.spawn == nil {
		cfg.spawn = Spawn
	}
	return &Pool{
		cfg:     cfg,
		helpers: map[string]*entry{},
		lru:     list.New(),
	}, nil
}

// Do runs one request as user.
func (p *Pool) Do(ctx context.Context, user string, req *wire.Request) (*wire.Response, *os.File, error) {
	e, err := p.acquire(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	defer p.release(e)
	return e.helper.Do(ctx, req)
}

// acquire returns a helper for user, starting or replacing one as needed, with
// its in-flight count already incremented.
func (p *Pool) acquire(ctx context.Context, user string) (*entry, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	e, cached := p.helpers[user]
	needCreds := !cached || time.Since(e.credsAt) >= p.cfg.CredsTTL
	p.mu.Unlock()

	// Resolve outside the lock: it may shell out to NSS, which on a domain-joined
	// host can block on the network, and holding the pool lock through that would
	// stall every other user.
	var creds Creds
	if needCreds {
		var err error
		if creds, err = p.cfg.Resolver.Resolve(ctx, user); err != nil {
			return nil, err
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, ErrClosed
	}

	e, cached = p.helpers[user]
	if cached {
		if !needCreds {
			return p.checkout(e), nil
		}
		e.creds, e.credsAt = creds, time.Now()
		if creds.SameGroups(e.helper.Creds()) {
			return p.checkout(e), nil
		}
		// The user's memberships changed. The running helper holds the old set for
		// its whole life, so a new team would be invisible — and, worse, a removed
		// one would still work. Replace it.
		p.retire(e)
	}

	if err := p.makeRoom(); err != nil {
		return nil, err
	}
	h, err := p.cfg.spawn(Spec{Bin: p.cfg.Bin, Root: p.cfg.Root, Creds: creds})
	if err != nil {
		return nil, err
	}
	e = &entry{user: user, helper: h, creds: creds, credsAt: time.Now()}
	e.elem = p.lru.PushFront(e)
	p.helpers[user] = e
	return p.checkout(e), nil
}

// checkout marks an entry busy and most-recently-used. Caller holds the lock.
func (p *Pool) checkout(e *entry) *entry {
	e.inUse++
	p.lru.MoveToFront(e.elem)
	return e
}

func (p *Pool) release(e *entry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e.inUse--
	if e.inUse == 0 {
		e.idleSince = time.Now()
		// A helper retired while it was busy is stopped by whoever drops the last
		// reference, so an in-flight request is never cut off mid-operation.
		if _, live := p.helpers[e.user]; !live {
			go e.helper.Stop()
		}
	}
}

// retire removes an entry from the pool, stopping it once it is idle. Caller
// holds the lock.
func (p *Pool) retire(e *entry) {
	delete(p.helpers, e.user)
	p.lru.Remove(e.elem)
	if e.inUse == 0 {
		go e.helper.Stop()
	}
}

// makeRoom enforces MaxHelpers. Caller holds the lock.
func (p *Pool) makeRoom() error {
	p.reapLocked()
	for len(p.helpers) >= p.cfg.MaxHelpers {
		victim := p.lruIdle()
		if victim == nil {
			// Every helper is serving something. Failing here is better than
			// queueing: the caller would be waiting on an unrelated user's work,
			// with no bound on how long.
			return fmt.Errorf("helperpool: all %d helpers are in use", p.cfg.MaxHelpers)
		}
		p.retire(victim)
	}
	return nil
}

// lruIdle returns the least recently used entry with nothing in flight.
func (p *Pool) lruIdle() *entry {
	for el := p.lru.Back(); el != nil; el = el.Prev() {
		if e := el.Value.(*entry); e.inUse == 0 {
			return e
		}
	}
	return nil
}

// Reap stops helpers that have been idle longer than IdleTimeout. Call it
// periodically; acquire also reaps before starting a new helper, so a pool that
// keeps being used stays trimmed on its own.
func (p *Pool) Reap() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reapLocked()
}

func (p *Pool) reapLocked() {
	cutoff := time.Now().Add(-p.cfg.IdleTimeout)
	for el := p.lru.Back(); el != nil; {
		e := el.Value.(*entry)
		el = el.Prev()
		if e.inUse == 0 && e.idleSince.Before(cutoff) {
			p.retire(e)
		}
	}
}

// Len reports how many helpers are currently running.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.helpers)
}

// Close stops every helper. In-flight requests are not interrupted; their
// helpers are stopped as they finish.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for el := p.lru.Back(); el != nil; {
		e := el.Value.(*entry)
		el = el.Prev()
		p.retire(e)
	}
	return nil
}
