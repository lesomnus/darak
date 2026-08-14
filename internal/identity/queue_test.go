package identity

import (
	"testing"
	"time"
)

func req(subject string, emails ...string) Request {
	return Request{Issuer: "https://idp", Subject: subject, Emails: emails, Name: "Somebody"}
}

// One row per person, however many times they try. The queue is a list of
// decisions to make, and repetition is a property of a row rather than another
// row.
func TestRecordDeduplicatesBySubject(t *testing.T) {
	q := NewQueue()
	for i := range 5 {
		if err := q.Record(req("obj-1", "a@example.com"), now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if q.Len() != 1 {
		t.Fatalf("Len() = %d; want 1", q.Len())
	}
	got, ok := q.Get("https://idp", "obj-1")
	if !ok {
		t.Fatal("the request is gone")
	}
	if got.Count != 5 {
		t.Errorf("Count = %d; want 5", got.Count)
	}
	if !got.First.Equal(now) {
		t.Errorf("First = %v; want the first attempt", got.First)
	}
	if !got.Last.Equal(now.Add(4 * time.Minute)) {
		t.Errorf("Last = %v; want the last attempt", got.Last)
	}
}

// A person who signs in with a second alias should not become a second row, and
// the operator should see both addresses on the one decision.
func TestRecordMergesAddresses(t *testing.T) {
	q := NewQueue()
	if err := q.Record(req("obj-1", "a@example.com"), now); err != nil {
		t.Fatal(err)
	}
	if err := q.Record(req("obj-1", "b@example.com"), now); err != nil {
		t.Fatal(err)
	}
	got, _ := q.Get("https://idp", "obj-1")
	if len(got.Emails) != 2 || got.Emails[0] != "a@example.com" || got.Emails[1] != "b@example.com" {
		t.Errorf("Emails = %v; want both, sorted", got.Emails)
	}
}

// Anybody the tenant authenticates can put a row in here without having an
// account, so the ceiling is what keeps it from being a table an employee can
// grow without limit.
func TestRecordEvictsTheLeastRecentAtTheCeiling(t *testing.T) {
	q := NewQueue()
	q.Max = 3

	for i := range 3 {
		if err := q.Record(req(string(rune('a'+i))), now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Record(req("d"), now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if q.Len() != 3 {
		t.Fatalf("Len() = %d; want the ceiling of 3", q.Len())
	}
	if _, ok := q.Get("https://idp", "a"); ok {
		t.Error("the least recently seen request survived")
	}
	if _, ok := q.Get("https://idp", "d"); !ok {
		t.Error("the newest request was dropped instead")
	}
}

func TestExpiredRequestsAreDropped(t *testing.T) {
	q := NewQueue()
	q.TTL = time.Hour

	if err := q.Record(req("old"), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := q.Record(req("new"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := q.List(); len(got) != 1 || got[0].Subject != "new" {
		t.Errorf("List() = %+v; want only the recent one", got)
	}
}

func TestRecordRefusesASubjectlessRequest(t *testing.T) {
	q := NewQueue()
	if err := q.Record(Request{Issuer: "https://idp", Emails: []string{"a@example.com"}}, now); err == nil {
		t.Fatal("queued a request with nothing to identify it by")
	}
}

// Unreviewed input must not be able to decide whether the server starts.
func TestFileQueueSurvivesAMalformedFile(t *testing.T) {
	path := t.TempDir() + "/pending.json"
	if err := replace(path, ".x-*", []byte("{ not json")); err != nil {
		t.Fatal(err)
	}
	q, err := NewFileQueue(path)
	if err == nil {
		t.Fatal("want the reason reported to the caller")
	}
	if q == nil {
		t.Fatal("want a usable empty queue despite the error")
	}
	if err := q.Record(req("obj"), now); err != nil {
		t.Fatalf("the queue is not usable: %v", err)
	}
}

func TestFileQueueRoundTrip(t *testing.T) {
	path := t.TempDir() + "/pending.json"
	q, err := NewFileQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Record(req("obj-1", "a@example.com"), time.Now()); err != nil {
		t.Fatal(err)
	}

	back, err := NewFileQueue(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := back.Get("https://idp", "obj-1")
	if !ok {
		t.Fatal("the request did not survive a restart")
	}
	if len(got.Emails) != 1 || got.Emails[0] != "a@example.com" {
		t.Errorf("Emails = %v", got.Emails)
	}
}
