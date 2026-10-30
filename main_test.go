package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/traP-jp/isutori/internal/queue"
)

func TestReadmeListsEveryDocPage(t *testing.T) {
	readme, err := readDoc(nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := docs.ReadDir("docs")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		for _, want := range []string{"(docs/" + e.Name() + ")", "`isutori docs " + name + "`"} {
			if !strings.Contains(string(readme), want) {
				t.Errorf("README does not mention %s", want)
			}
		}
		if _, err := readDoc([]string{name}); err != nil {
			t.Errorf("readDoc(%q): %v", name, err)
		}
	}
}

func TestReadDocRejectsUnknownPage(t *testing.T) {
	for _, name := range []string{"missing", "../README", "hooks.md"} {
		if _, err := readDoc([]string{name}); err == nil {
			t.Errorf("readDoc(%q) succeeded", name)
		}
	}
}

func TestStatusShowsRunsThatAreFinishing(t *testing.T) {
	queued := []queue.Entry{{RunID: "next", Requester: "b", Ref: "feature", Mode: "measure", EnqueuedAt: time.Now(), LeaseAge: 5 * time.Second}}
	finishing := []queue.Entry{{RunID: "done", Requester: "a", Ref: "main", Mode: "final", LeaseAge: 90 * time.Second}}

	var table bytes.Buffer
	writeStatusTable(&table, queued, finishing, nil)
	for _, want := range []string{"next", "finishing (the servers are free", "done", "a", "1m30s ago"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, table.String())
		}
	}
	table.Reset()
	writeStatusTable(&table, queued, nil, nil)
	if strings.Contains(table.String(), "finishing") {
		t.Errorf("a finishing section without finishing runs:\n%s", table.String())
	}

	var out bytes.Buffer
	if err := writeStatusJSON(&out, queued, finishing, nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Queue []struct {
			RunID string `json:"run_id"`
		}
		Finishing []struct {
			RunID           string  `json:"run_id"`
			ReleasedSeconds float64 `json:"released_seconds"`
		}
		Deployed json.RawMessage
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(doc.Queue) != 1 || len(doc.Finishing) != 1 || doc.Finishing[0].RunID != "done" || doc.Finishing[0].ReleasedSeconds != 90 || string(doc.Deployed) != "null" {
		t.Fatalf("unexpected document:\n%s", out.String())
	}
	out.Reset()
	if err := writeStatusJSON(&out, nil, nil, nil, time.Minute); err != nil || !strings.Contains(out.String(), `"finishing": []`) {
		t.Fatalf("an empty list must be [], not null: %v\n%s", err, out.String())
	}
}
