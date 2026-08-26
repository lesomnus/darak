package identity

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lesomnus/darak/internal/statefile"
)

// Defaults for the queue's two bounds. See Queue.
const (
	DefaultRequestTTL = 14 * 24 * time.Hour
	DefaultMaxPending = 500
)

// Request is an identity that presented itself and has no mapping.
//
// It grants nothing. Recording it is what turns "the administrator must type
// this person's address correctly, in advance, from memory" into "the person
// signs in once and the administrator picks their account from a list" — which
// removes the entire class of mistake where a mistyped address produces a
// sign-in failure indistinguishable from a wrong password.
type Request struct {
	Issuer  string   `json:"issuer"`
	Subject string   `json:"subject"`
	Emails  []string `json:"emails"`
	// Name is what the provider calls this person, shown in the queue so an
	// operator can tell who they are looking at. It is display text and nothing
	// else — never matched against anything.
	Name string `json:"name,omitempty"`

	First time.Time `json:"first_seen"`
	Last  time.Time `json:"last_seen"`
	Count int       `json:"count"`
}

// Queue holds identities waiting for an operator.
//
// Anyone the configured tenant will authenticate can put a row in here without
// having an account on this server, so it is bounded in both directions: one
// row per directory object however many times they try, a ceiling on how many
// rows exist, and an age after which a row is dropped. Without those it is a
// table any employee can grow without limit, on the same volume as the share
// store.
//
// What it holds is also kept to the minimum an operator needs to make the
// decision: who, from where, which addresses, when, how often. Not the token,
// not the rest of the claims — these are people who may never become users of
// this system, and the less of them that is written down the less there is to
// delete later.
type Queue struct {
	// Save persists the current set. Used only in the in-memory mode tests run
	// in; a file-backed queue (NewFileQueue) persists through guard instead.
	Save func(requests []Request) error

	// TTL and Max bound the queue. Zero means the defaults above.
	TTL time.Duration
	Max int

	// guard coordinates the state file across replicas. Nil means in-memory only.
	guard *statefile.Guard

	mu sync.Mutex
	m  map[string]*Request
}

func NewQueue() *Queue {
	return &Queue{m: map[string]*Request{}}
}

func (q *Queue) ttl() time.Duration {
	if q.TTL <= 0 {
		return DefaultRequestTTL
	}
	return q.TTL
}

func (q *Queue) max() int {
	if q.Max <= 0 {
		return DefaultMaxPending
	}
	return q.Max
}

// Load replaces the contents from JSON.
//
// Unlike the mapping store, a malformed queue is NOT fatal: it grants nothing,
// so starting empty costs at most a few people signing in again to re-queue
// themselves. Refusing to start over it would let unreviewed input — which is
// what this is — decide whether the file server comes up.
func (q *Queue) Load(data []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.loadLocked(data)
}

// loadLocked is Load's body; the caller holds the lock. guard calls it to reload
// the queue when another replica has written.
func (q *Queue) loadLocked(data []byte) error {
	var list []Request
	if len(data) > 0 {
		if err := json.Unmarshal(data, &list); err != nil {
			return fmt.Errorf("identity: parse pending requests: %w", err)
		}
	}
	q.m = map[string]*Request{}
	for i := range list {
		r := list[i]
		if r.Subject == "" {
			continue
		}
		q.m[subjectKey(r.Issuer, r.Subject)] = &r
	}
	return nil
}

// Marshal renders the queue, oldest first.
func (q *Queue) Marshal() ([]byte, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.marshalLocked()
}

func (q *Queue) marshalLocked() ([]byte, error) {
	return json.MarshalIndent(q.list(), "", "  ")
}

// fresh reloads from the shared file if another replica has written. Caller
// holds the lock.
func (q *Queue) fresh() {
	_ = q.guard.Fresh(q.loadLocked)
}

// commit runs a mutation under the in-process lock and persists it, as a
// cross-replica read-modify-write when a file backs the queue and as the
// mutation plus optional Save when none does. mutate returns whether anything
// changed.
func (q *Queue) commit(mutate func() (bool, error)) error {
	q.mu.Lock()
	if q.guard != nil {
		defer q.mu.Unlock()
		return q.guard.Write(q.loadLocked, mutate, q.marshalLocked)
	}
	changed, err := mutate()
	if err != nil || !changed || q.Save == nil {
		q.mu.Unlock()
		return err
	}
	list := q.list()
	q.mu.Unlock()
	return q.Save(list)
}

// list copies the queue, most recent first. Callers must hold the lock.
func (q *Queue) list() []Request {
	out := make([]Request, 0, len(q.m))
	for _, r := range q.m {
		c := *r
		c.Emails = append([]string(nil), r.Emails...)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// List returns the pending requests, most recently seen first.
func (q *Queue) List() []Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.fresh()
	q.sweep(time.Now())
	return q.list()
}

// Get returns one request.
func (q *Queue) Get(issuer, subject string) (Request, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.fresh()
	r, ok := q.m[subjectKey(issuer, subject)]
	if !ok {
		return Request{}, false
	}
	c := *r
	c.Emails = append([]string(nil), r.Emails...)
	return c, true
}

// Record notes that an identity presented itself with no mapping.
//
// The same person signing in ten times is one row with a count, not ten rows:
// the queue is a list of people to decide about, and repetition is a property
// of a row rather than another row.
func (q *Queue) Record(req Request, now time.Time) error {
	if req.Subject == "" {
		return fmt.Errorf("identity: refusing to queue a request with no subject")
	}
	return q.commit(func() (bool, error) {
		q.sweep(now)

		k := subjectKey(req.Issuer, req.Subject)
		if have, ok := q.m[k]; ok {
			have.Last = now
			have.Count++
			have.Name = req.Name
			have.Emails = mergeStrings(have.Emails, req.Emails)
			return true, nil
		}

		// The ceiling drops the least recently seen, not the newest arrival:
		// somebody trying right now is more likely to be a person waiting for an
		// answer than a row from two weeks ago that nobody acted on.
		for len(q.m) >= q.max() {
			var oldestKey string
			var oldest time.Time
			for k, r := range q.m {
				if oldestKey == "" || r.Last.Before(oldest) {
					oldestKey, oldest = k, r.Last
				}
			}
			delete(q.m, oldestKey)
		}

		req.First, req.Last, req.Count = now, now, 1
		req.Emails = mergeStrings(nil, req.Emails)
		q.m[k] = &req
		return true, nil
	})
}

// Discard removes a request without approving it.
func (q *Queue) Discard(issuer, subject string) error {
	return q.commit(func() (bool, error) {
		k := subjectKey(issuer, subject)
		if _, ok := q.m[k]; !ok {
			return false, fmt.Errorf("%w: no such request", ErrNoMapping)
		}
		delete(q.m, k)
		return true, nil
	})
}

// Sweep drops expired requests.
func (q *Queue) Sweep() error {
	return q.commit(func() (bool, error) {
		before := len(q.m)
		q.sweep(time.Now())
		return before != len(q.m), nil
	})
}

// sweep drops expired requests. Callers must hold the lock.
func (q *Queue) sweep(now time.Time) {
	cutoff := now.Add(-q.ttl())
	for k, r := range q.m {
		if r.Last.Before(cutoff) {
			delete(q.m, k)
		}
	}
}

// Len reports how many requests are waiting.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.fresh()
	return len(q.m)
}

// mergeStrings adds normalised addresses to a set, keeping it sorted.
func mergeStrings(have, add []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range append(append([]string(nil), have...), add...) {
		s = NormalizeEmail(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
