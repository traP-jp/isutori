package userscript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentity(t *testing.T) {
	script, err := Render([]string{"http://portal.example/*"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := FileSource(script)
	if err != nil {
		t.Fatal(err)
	}
	id, err := SourceID(source)
	if err != nil {
		t.Fatal(err)
	}
	fromCRLF, err := FileSource(strings.ReplaceAll(script, "\n", "\r\n"))
	if err != nil || fromCRLF != source {
		t.Fatalf("file line endings: %v", err)
	}
	crlf, err := SourceID(strings.ReplaceAll(source, "\n", "\r\n"))
	if err != nil || crlf != id {
		t.Fatalf("line endings: %s %v", crlf, err)
	}
	changed, err := SourceID(strings.Replace(source, "async prepare(target)", "async prepare(target /* edited */)", 1))
	if err != nil || changed == id {
		t.Fatalf("adapter edit not detected: %s %v", changed, err)
	}
	if _, err := FileSource("alert('before');\n" + script); err == nil {
		t.Fatal("ignored code before metadata")
	}
	if _, err := FileSource(script + "\nalert('outside');"); err == nil {
		t.Fatal("ignored code outside wrapper")
	}
	if _, err := FileSource("// ==/UserScript==\n(()=>{})();"); err == nil {
		t.Fatal("accepted old wrapper")
	}
	if _, err := SourceID("function isutoriScript() { [native code] }"); err == nil {
		t.Fatal("accepted unavailable source")
	}
	path := filepath.Join(t.TempDir(), "script.user.js")
	if v := ReadReference(path).Verify(source); v.Status != "unknown" || v.Error == "" {
		t.Fatalf("missing reference: %+v", v)
	}
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if v := ReadReference(path).Verify(source); v.Status != "matched" || v.Expected != id || v.Actual != id {
		t.Fatalf("matching reference: %+v", v)
	}
	if v := ReadReference(path).Compare(changed); v.Status != "mismatched" {
		t.Fatalf("mismatch: %+v", v)
	}
}
