package userscript

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

const DefaultPath = "ops/isutori.user.js"

// Keep these in sync with the wrapper in isutori.user.js.
const (
	functionStart = "function isutoriScript() {"
	wrapperStart  = "(" + functionStart
	wrapperEnd    = ")();"
)

type Status string

// The same strings appear in isutori.user.js, and in bench.json and run.json.
const (
	Matched    Status = "matched"
	Mismatched Status = "mismatched"
	Unknown    Status = "unknown"
	NotChecked Status = "not_checked"
)

type Verification struct {
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Status   Status `json:"status"`
	Error    string `json:"error,omitempty"`
}

// Reference is the identity of the userscript file that the running script
// must match.
type Reference struct {
	ID  string
	Err error
}

func normalize(source string) string {
	return strings.ReplaceAll(strings.ReplaceAll(source, "\r\n", "\n"), "\r", "\n")
}

// SourceID identifies the source of the isutoriScript function, as the browser
// reports it.
func SourceID(source string) (string, error) {
	source = normalize(source)
	if source == functionStart+" [native code] }" {
		return "", errors.New("the browser did not expose the userscript source")
	}
	if !strings.HasPrefix(source, functionStart) || !strings.HasSuffix(source, "}") {
		return "", errors.New("expected the named isutoriScript function; regenerate the userscript template")
	}
	sum := sha256.Sum256([]byte(source))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// FileSource extracts the isutoriScript function from a userscript file.
func FileSource(script string) (string, error) {
	header, body, ok := strings.Cut(normalize(script), "// ==/UserScript==")
	if !ok {
		return "", errors.New("userscript metadata is missing")
	}
	for line := range strings.Lines(header) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "//") {
			return "", errors.New("code outside the isutoriScript wrapper is not supported")
		}
	}
	body = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body), "// @ts-check"))
	if !strings.HasPrefix(body, wrapperStart) || !strings.HasSuffix(body, wrapperEnd) {
		return "", errors.New("expected the isutoriScript wrapper; regenerate the userscript template")
	}
	return strings.TrimSuffix(strings.TrimPrefix(body, "("), wrapperEnd), nil
}

func ReadReference(path string) Reference {
	b, err := os.ReadFile(path)
	if err != nil {
		return Reference{Err: err}
	}
	source, err := FileSource(string(b))
	if err != nil {
		return Reference{Err: err}
	}
	id, err := SourceID(source)
	return Reference{ID: id, Err: err}
}

// Compare checks the ID of the running script against the reference.
func (r Reference) Compare(actual string) Verification {
	v := Verification{Expected: r.ID, Actual: actual, Status: Unknown}
	switch {
	case r.Err != nil:
		v.Error = r.Err.Error()
	case actual == "":
	case actual == r.ID:
		v.Status = Matched
	default:
		v.Status = Mismatched
	}
	return v
}

// Verify checks the source of the running script against the reference.
func (r Reference) Verify(source string) Verification {
	actual, err := SourceID(source)
	v := r.Compare(actual)
	if err != nil {
		v.Error = err.Error()
	}
	return v
}
