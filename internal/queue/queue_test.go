package queue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
		n, err := q.Enqueue(ctx, Ticket{ID: id, Requester: "risu-risu's"})
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}

	entries, err := q.List(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Ticket.ID != "a" || entries[2].Ticket.ID != "c" {
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
	if len(entries) != 2 || entries[0].Ticket.ID != "b" {
		t.Fatalf("expired ticket was not pruned: %+v", entries)
	}
	if err := q.Renew(ctx, names[0]); !errors.Is(err, ErrTicketLost) {
		t.Fatalf("renewing a pruned ticket: got %v, want ErrTicketLost", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".isutori/queue", names[0])); !os.IsNotExist(err) {
		t.Fatal("renew recreated a pruned ticket")
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
