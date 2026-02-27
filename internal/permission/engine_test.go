package permission

import "testing"

func TestDecidePriority(t *testing.T) {
	e := NewEngine(DecisionAsk)
	rules := map[string]string{
		"*":      "deny",
		"read":   "allow",
		"mcp:*":  "ask",
		"mcp:fs": "allow",
	}

	dec, matched := e.Decide("read", rules)
	if dec != DecisionAllow || matched != "read" {
		t.Fatalf("expected exact allow, got %s matched=%s", dec, matched)
	}

	dec, matched = e.Decide("mcp:git", rules)
	if dec != DecisionAsk || matched != "mcp:*" {
		t.Fatalf("expected wildcard ask, got %s matched=%s", dec, matched)
	}

	dec, matched = e.Decide("unknown", rules)
	if dec != DecisionDeny || matched != "*" {
		t.Fatalf("expected fallback deny, got %s matched=%s", dec, matched)
	}
}
