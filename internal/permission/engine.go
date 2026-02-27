package permission

import (
	"path"
	"strings"
)

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

type Engine struct {
	defaultDecision Decision
}

func NewEngine(defaultDecision Decision) *Engine {
	if defaultDecision == "" {
		defaultDecision = DecisionAsk
	}
	return &Engine{defaultDecision: defaultDecision}
}

// Decide applies priority: exact > wildcard > default.
func (e *Engine) Decide(toolName string, rules map[string]string) (decision Decision, matched string) {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	if toolName == "" {
		return DecisionDeny, ""
	}

	if len(rules) == 0 {
		return e.defaultDecision, "default"
	}

	if v, ok := rules[toolName]; ok {
		return normalizeDecision(v, e.defaultDecision), toolName
	}

	for pattern, v := range rules {
		p := strings.ToLower(strings.TrimSpace(pattern))
		if p == "" || p == toolName {
			continue
		}
		if p == "*" {
			continue
		}
		if ok, _ := path.Match(p, toolName); ok {
			return normalizeDecision(v, e.defaultDecision), p
		}
	}

	if v, ok := rules["*"]; ok {
		return normalizeDecision(v, e.defaultDecision), "*"
	}

	return e.defaultDecision, "default"
}

func normalizeDecision(v string, fallback Decision) Decision {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "allow":
		return DecisionAllow
	case "deny":
		return DecisionDeny
	case "ask":
		return DecisionAsk
	default:
		return fallback
	}
}
