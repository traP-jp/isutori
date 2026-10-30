package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newQueue(t *testing.T) (*Queue, string) {
	home := t.TempDir()
	return &Queue{Shell: Local{Dir: home}, Dir: ".isutori", TTL: 10 * time.Second}, home
}

func TestQueue(t *testing.T) {
	ctx := context.Background()
	q, home := newQueue(t)

	var names []string
	for _, id := range []string{"a", "b", "c"} {
		n, err := q.Enqueue(ctx, Entry{RunID: id, Requester: "risu-risu's"})
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}

	entries, err := q.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].RunID != "a" || entries[2].RunID != "c" {
		t.Fatalf("unexpected order: %+v", entries)
	}

	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(home, ".isutori/queue", names[0]), old, old); err != nil {
		t.Fatal(err)
	}
	if err := q.Renew(ctx, names[1]); err != nil {
		t.Fatal(err)
	}
	entries, err = q.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].RunID != "b" {
		t.Fatalf("expired entry was not pruned: %+v", entries)
	}
	if err := q.Renew(ctx, names[0]); !errors.Is(err, ErrEntryLost) {
		t.Fatalf("renewing a pruned entry: got %v, want ErrEntryLost", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".isutori/queue", names[0])); !os.IsNotExist(err) {
		t.Fatal("renew recreated a pruned entry")
	}

	if err := q.Remove(ctx, names[1]); err != nil {
		t.Fatal(err)
	}
	if err := q.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if entries, _ := q.List(ctx, false); len(entries) != 0 {
		t.Fatalf("queue not empty after clear: %+v", entries)
	}
}

func TestDeployed(t *testing.T) {
	ctx := context.Background()
	q, _ := newQueue(t)
	if got, err := q.ReadDeployed(ctx); err != nil || got != nil {
		t.Fatalf("got %s, %v; want nil, nil", got, err)
	}
	if err := q.WriteDeployed(ctx, []byte(`{"app_commit":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if got, err := q.ReadDeployed(ctx); err != nil || string(got) != `{"app_commit":"x"}` {
		t.Fatalf("got %s, %v", got, err)
	}
}

func TestRunReturnsWhenAChildHoldsThePipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Local{}.Run(ctx, "sleep 30 & wait", nil)
	if err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
}

func TestOrderSurvivesClockSteppingBack(t *testing.T) {
	bin := t.TempDir()
	fake := "#!/bin/sh\nf=" + filepath.Join(bin, "now") + "\nn=$(cat \"$f\" 2>/dev/null || echo 900)\necho $((n - 1)) > \"$f\"\necho \"$n\"\n"
	if err := os.WriteFile(filepath.Join(bin, "date"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := context.Background()
	q, _ := newQueue(t)
	for _, id := range []string{"first", "second", "third"} {
		if _, err := q.Enqueue(ctx, Entry{RunID: id, Requester: "r"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := queuedIDs(t, q); got != "first,second,third" {
		t.Fatalf("queue order %s, want arrival order", got)
	}
}

func TestOrderWithEntriesFromClockNamingVersions(t *testing.T) {
	ctx := context.Background()
	q, home := newQueue(t)
	if _, err := q.Enqueue(ctx, Entry{RunID: "new1", Requester: "r"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	old := fmt.Sprintf("%d-r-old.json", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(home, ".isutori/queue", old), []byte(`{"run_id":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(ctx, Entry{RunID: "new2", Requester: "r"}); err != nil {
		t.Fatal(err)
	}
	if got := queuedIDs(t, q); got != "new1,old,new2" {
		t.Fatalf("queue order %s, want arrival order", got)
	}
}

func queuedIDs(t *testing.T, q *Queue) string {
	entries, err := q.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.RunID)
	}
	return strings.Join(ids, ",")
}

func TestReleaseLeavesAMarkerUntilItEnds(t *testing.T) {
	ctx := context.Background()
	q, home := newQueue(t)
	first, err := q.Enqueue(ctx, Entry{RunID: "a", Requester: "x"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Enqueue(ctx, Entry{RunID: "b", Requester: "y"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Release(ctx, first, Entry{RunID: "a", Requester: "x", Ref: "main"}); err != nil {
		t.Fatal(err)
	}
	if queued := queuedIDs(t, q); queued != "b" {
		t.Fatalf("queue after release = %q, want only the next run", queued)
	}
	finishing, err := q.ListFinishing(ctx)
	if err != nil || len(finishing) != 1 || finishing[0].RunID != "a" || finishing[0].Ref != "main" {
		t.Fatalf("finishing = %+v, %v", finishing, err)
	}
	if err := q.Renew(ctx, first); !errors.Is(err, ErrEntryLost) {
		t.Fatalf("renewing a released entry: %v, want ErrEntryLost", err)
	}
	if err := q.EndFinishing(ctx, first); err != nil {
		t.Fatal(err)
	}
	if finishing, _ := q.ListFinishing(ctx); len(finishing) != 0 {
		t.Fatalf("marker survived EndFinishing: %+v", finishing)
	}
	if queued := queuedIDs(t, q); queued != "b" {
		t.Fatalf("queue = %q, want b", queued)
	}

	// A marker whose run died is dropped once it is old enough.
	if err := q.Release(ctx, second, Entry{RunID: "b"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-finishingMaxAge - time.Minute)
	if err := os.Chtimes(filepath.Join(home, ".isutori/finishing", second), old, old); err != nil {
		t.Fatal(err)
	}
	if finishing, _ := q.ListFinishing(ctx); len(finishing) != 0 {
		t.Fatalf("a stale marker was listed: %+v", finishing)
	}
	if _, err := os.Stat(filepath.Join(home, ".isutori/finishing", second)); !os.IsNotExist(err) {
		t.Fatal("a stale marker was not deleted")
	}
}

func TestClearAlsoClearsMarkers(t *testing.T) {
	ctx := context.Background()
	q, _ := newQueue(t)
	name, err := q.Enqueue(ctx, Entry{RunID: "a", Requester: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Release(ctx, name, Entry{RunID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := q.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if finishing, _ := q.ListFinishing(ctx); len(finishing) != 0 {
		t.Fatalf("markers after clear: %+v", finishing)
	}
}

func TestWaitedIsCountedOnTheQueueHost(t *testing.T) {
	ctx := context.Background()
	q, home := newQueue(t)
	if _, err := q.Enqueue(ctx, Entry{RunID: "a", Requester: "x", EnqueuedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	entries, err := q.List(ctx, false)
	if err != nil || len(entries) != 1 {
		t.Fatalf("%+v, %v", entries, err)
	}
	// The requester's clock said an hour ago; the queue host's clock says about a second.
	if w := entries[0].Waited; w < time.Second || w > 5*time.Second {
		t.Fatalf("waited %v, want about 1s", w)
	}

	// An entry whose name does not start with a counter must not break the listing.
	odd := filepath.Join(home, ".isutori/queue/odd-name.json")
	if err := os.WriteFile(odd, []byte(`{"run_id":"b"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err = q.List(ctx, false)
	if err != nil || len(entries) != 2 {
		t.Fatalf("%+v, %v", entries, err)
	}
	for _, e := range entries {
		if e.RunID == "b" && e.Waited != 0 {
			t.Fatalf("waited %v for an entry without a counter", e.Waited)
		}
	}
}
