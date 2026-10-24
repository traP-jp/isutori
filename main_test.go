package main

import (
	"strings"
	"testing"
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
