package userscript

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

const DefaultPath = "ops/isutori.user.js"

// Keep the wrapper signature in sync with isutori.user.js.
const functionStart = "function isutoriScript() {"

type Verification struct {
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

type Reference struct {
	ID    string
	Error string
}

func normalize(source string) string {
	return strings.ReplaceAll(strings.ReplaceAll(source, "\r\n", "\n"), "\r", "\n")
}

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

func FileSource(script string) (string, error) {
	script = normalize(script)
	header, body, ok := strings.Cut(script, "// ==/UserScript==")
	if !ok {
		return "", errors.New("userscript metadata is missing")
	}
	for _, line := range strings.Split(header, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "//") {
			return "", errors.New("code outside the isutoriScript wrapper is not supported")
		}
	}
	body = strings.TrimSpace(body)
	body = strings.TrimSpace(strings.TrimPrefix(body, "// @ts-check"))
	if !strings.HasPrefix(body, "("+functionStart) || !strings.HasSuffix(body, "})();") {
		return "", errors.New("expected the isutoriScript wrapper; regenerate the userscript template")
	}
	return body[1 : len(body)-4], nil
}

func ReadReference(path string) Reference {
	b, err := os.ReadFile(path)
	if err != nil {
		return Reference{Error: err.Error()}
	}
	source, err := FileSource(string(b))
	if err != nil {
		return Reference{Error: err.Error()}
	}
	id, err := SourceID(source)
	if err != nil {
		return Reference{Error: err.Error()}
	}
	return Reference{ID: id}
}

func (r Reference) Compare(actual string) Verification {
	v := Verification{Expected: r.ID, Actual: actual, Status: "unknown", Error: r.Error}
	if r.Error != "" || r.ID == "" || actual == "" {
		return v
	}
	v.Status = "mismatched"
	if r.ID == actual {
		v.Status = "matched"
	}
	return v
}

func (r Reference) Verify(source string) Verification {
	actual, err := SourceID(source)
	v := r.Compare(actual)
	if err != nil {
		v.Error = err.Error()
	}
	return v
}
