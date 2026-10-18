package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/traP-jp/isutori/internal/config"
)

var ErrTicketLost = errors.New("ticket no longer exists in the queue (expired or cleared)")

type Ticket struct {
	ID         string    `json:"id"`
	Requester  string    `json:"requester"`
	Ref        string    `json:"ref"`
	AppCommit  string    `json:"app_commit"`
	Mode       string    `json:"mode"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}

type Entry struct {
	Name     string        `json:"name"`
	LeaseAge time.Duration `json:"lease_age"`
	Ticket   Ticket        `json:"ticket"`
}

// Queue keeps one file per ticket under Dir/queue on the queue host.
// File names start with the host's clock in nanoseconds, so lexical order is
// arrival order regardless of clock skew between clients. A ticket's mtime is
// its lease; clients touch it periodically.
type Queue struct {
	Shell Shell
	Dir   string
	TTL   time.Duration
}

const exitLost = 3

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_]`)

func (q *Queue) prelude() string {
	return fmt.Sprintf("set -e\nD=%s\nQ=\"$D/queue\"\nmkdir -p \"$Q\"\nexec 9>\"$D/lock\"\nflock 9\n", Quote(q.Dir))
}

func (q *Queue) Enqueue(ctx context.Context, t Ticket) (string, error) {
	body, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	suffix := unsafeName.ReplaceAllString(t.Requester, "_") + "-" + unsafeName.ReplaceAllString(t.ID, "_") + ".json"
	script := q.prelude() + fmt.Sprintf(`n="$(date +%%s%%N)-"%s
cat > "$Q/.$n.tmp"
mv "$Q/.$n.tmp" "$Q/$n"
printf %%s "$n"
`, Quote(suffix))
	out, err := q.Shell.Run(ctx, script, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("enqueue: %w", err)
	}
	return string(out), nil
}

func (q *Queue) Renew(ctx context.Context, name string) error {
	script := q.prelude() + fmt.Sprintf("f=\"$Q/\"%s\ntouch -c \"$f\"\n[ -e \"$f\" ] || exit %d\n", Quote(name), exitLost)
	_, err := q.Shell.Run(ctx, script, nil)
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) && exitErr.ExitCode() == exitLost {
		return ErrTicketLost
	}
	if err != nil {
		return fmt.Errorf("renew %s: %w", name, err)
	}
	return nil
}

func (q *Queue) Remove(ctx context.Context, name string) error {
	if _, err := q.Shell.Run(ctx, q.prelude()+"rm -f \"$Q/\""+Quote(name)+"\n", nil); err != nil {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

func (q *Queue) Clear(ctx context.Context) error {
	if _, err := q.Shell.Run(ctx, q.prelude()+"rm -f \"$Q\"/*.json\n", nil); err != nil {
		return fmt.Errorf("clear: %w", err)
	}
	return nil
}

// List returns the tickets in arrival order. With prune, tickets whose lease
// has expired are deleted instead of returned.
func (q *Queue) List(ctx context.Context, prune bool) ([]Entry, error) {
	ttl := -1
	if prune {
		ttl = int(q.TTL.Seconds())
	}
	script := q.prelude() + fmt.Sprintf(`now=$(date +%%s)
for f in "$Q"/*.json; do
  [ -e "$f" ] || continue
  age=$((now - $(stat -c %%Y "$f")))
  if [ %d -ge 0 ] && [ "$age" -gt %d ]; then rm -f "$f"; continue; fi
  printf '%%s\t%%s\t' "${f##*/}" "$age"
  tr -d '\n' < "$f"
  echo
done
`, ttl, ttl)
	out, err := q.Shell.Run(ctx, script, nil)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	var entries []Entry
	for line := range strings.Lines(string(out)) {
		name, rest, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "\t")
		ageStr, body, _ := strings.Cut(rest, "\t")
		age, err := strconv.Atoi(ageStr)
		if err != nil {
			return nil, fmt.Errorf("list: malformed line %q", line)
		}
		e := Entry{Name: name, LeaseAge: time.Duration(age) * time.Second}
		if err := json.Unmarshal([]byte(body), &e.Ticket); err != nil {
			return nil, fmt.Errorf("list: ticket %s: %w", name, err)
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

func (q *Queue) WriteDeployed(ctx context.Context, body []byte) error {
	script := q.prelude() + "cat > \"$D/deployed.json.tmp\"\nmv \"$D/deployed.json.tmp\" \"$D/deployed.json\"\n"
	if _, err := q.Shell.Run(ctx, script, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("write deployed.json: %w", err)
	}
	return nil
}

// ReadDeployed returns nil when nothing has been deployed through isutori yet.
func (q *Queue) ReadDeployed(ctx context.Context) (json.RawMessage, error) {
	out, err := q.Shell.Run(ctx, q.prelude()+"cat \"$D/deployed.json\" 2>/dev/null || true\n", nil)
	if err != nil {
		return nil, fmt.Errorf("read deployed.json: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	return out, nil
}

func New(c config.Queue) *Queue {
	return &Queue{
		Shell: SSH{Command: strings.Fields(c.SSHCommand), Host: c.Host},
		Dir:   c.Dir,
		TTL:   c.LeaseTTL,
	}
}
