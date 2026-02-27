package agent

import (
	"fmt"
	"sort"
	"strings"

	"go-agent/internal/config"
)

type Definition struct {
	Name        string
	Mode        string
	Model       string
	Tools       []string
	Permissions map[string]string
	Temperature float64
	MaxTokens   int
}

type Manager struct {
	agents       map[string]Definition
	defaultAgent string
}

func NewManager(cfg config.Config) (*Manager, error) {
	agents := make(map[string]Definition, len(cfg.Agents))
	for name, ac := range cfg.Agents {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		agents[key] = Definition{
			Name:        key,
			Mode:        strings.TrimSpace(ac.Mode),
			Model:       strings.TrimSpace(ac.Model),
			Tools:       normalizeStringSlice(ac.Tools),
			Permissions: normalizeMapKeys(ac.Permissions),
			Temperature: ac.Temperature,
			MaxTokens:   ac.MaxTokens,
		}
	}
	def := strings.ToLower(strings.TrimSpace(cfg.DefaultAgent))
	if _, ok := agents[def]; !ok {
		return nil, fmt.Errorf("default agent %q not found", cfg.DefaultAgent)
	}
	return &Manager{
		agents:       agents,
		defaultAgent: def,
	}, nil
}

func (m *Manager) Default() Definition {
	return m.agents[m.defaultAgent]
}

func (m *Manager) List() []string {
	out := make([]string, 0, len(m.agents))
	for name := range m.agents {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) Resolve(name string) (Definition, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return m.Default(), nil
	}
	a, ok := m.agents[name]
	if !ok {
		return Definition{}, fmt.Errorf("agent %q not found", name)
	}
	return a, nil
}

func (m *Manager) ParseAtAgent(input string, fallback string) (agentName string, cleaned string) {
	cleaned = strings.TrimSpace(input)
	if cleaned == "" {
		if strings.TrimSpace(fallback) == "" {
			return m.defaultAgent, ""
		}
		return strings.ToLower(strings.TrimSpace(fallback)), ""
	}
	if strings.HasPrefix(cleaned, "@") {
		first := strings.Fields(cleaned)
		if len(first) > 0 {
			candidate := strings.TrimPrefix(first[0], "@")
			if _, ok := m.agents[strings.ToLower(candidate)]; ok {
				rest := strings.TrimSpace(strings.TrimPrefix(cleaned, first[0]))
				return strings.ToLower(candidate), rest
			}
		}
	}
	if strings.TrimSpace(fallback) == "" {
		return m.defaultAgent, cleaned
	}
	return strings.ToLower(strings.TrimSpace(fallback)), cleaned
}

func normalizeStringSlice(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func normalizeMapKeys(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{"*": "ask"}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		val := strings.ToLower(strings.TrimSpace(v))
		if val == "" {
			val = "ask"
		}
		out[key] = val
	}
	if len(out) == 0 {
		out["*"] = "ask"
	}
	return out
}
