// Package userscript holds the Tampermonkey script that drives the portal.
package userscript

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed isutori.user.js
var source string

const defaultMatch = "// @match        http://127.0.0.1:7879/*\n"

// Render returns the userscript with its @match lines set to patterns.
func Render(patterns []string) (string, error) {
	if !strings.Contains(source, defaultMatch) {
		return "", fmt.Errorf("userscript: @match line not found")
	}
	var b strings.Builder
	for _, p := range patterns {
		fmt.Fprintf(&b, "// @match        %s\n", p)
	}
	return strings.Replace(source, defaultMatch, b.String(), 1), nil
}
