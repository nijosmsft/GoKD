// Parsers for debugger-style addresses and counts.
package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nijosmsft/gokd"
)

func parseAddr(s gokd.Session, text string) (uint64, error) {
	t := strings.TrimSpace(text)
	lower := strings.ToLower(t)
	switch {
	case strings.HasPrefix(lower, "0d"):
		return strconv.ParseUint(t[2:], 10, 64)
	case strings.HasPrefix(lower, "0x"):
		return strconv.ParseUint(t[2:], 16, 64)
	case isAllHex(t):
		return strconv.ParseUint(t, 16, 64)
	case strings.Contains(t, "!"):
		return s.NameToAddr(t)
	default:
		return 0, fmt.Errorf("invalid address %q", text)
	}
}

func parseCount(text string) (uint64, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return 0, fmt.Errorf("empty count")
	}
	if t[0] == 'l' || t[0] == 'L' {
		return strconv.ParseUint(t[1:], 16, 64)
	}
	return strconv.ParseUint(t, 10, 64)
}

func looksHex(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(strings.ToLower(t), "0x") || isAllHex(t)
}

func isAllHex(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
