package context

import (
	"os"
	"strings"
)

func LoadRules(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
