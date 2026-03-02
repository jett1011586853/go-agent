package session

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go-agent/internal/message"
	"go-agent/internal/storage"
)

const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
)

type Session struct {
	ID             string         `json:"id"`
	Turns          []message.Turn `json:"turns"`
	ArchivedTurns  []message.Turn `json:"archived_turns,omitempty"`
	WorkingSummary string         `json:"working_summary,omitempty"`
	PinnedFacts    []string       `json:"pinned_facts,omitempty"`
	ActiveRoot     string         `json:"active_root,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type Approval struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	TurnID     string    `json:"turn_id"`
	ToolName   string    `json:"tool_name"`
	Args       string    `json:"args"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
}

type Manager struct {
	store             storage.Store
	compactionTurns   int
	compactionTokens  int
	keepRecentTurns   int
	summaryMaxRunes   int
	pinnedFactsMaxLen int
}

func NewManager(store storage.Store, compactionTurns int, compactionTokens int) *Manager {
	if compactionTurns <= 0 {
		compactionTurns = 30
	}
	keep := compactionTurns / 3
	if keep < 4 {
		keep = 4
	}
	if keep >= compactionTurns {
		keep = compactionTurns - 1
	}
	if keep < 1 {
		keep = 1
	}
	if compactionTokens < 0 {
		compactionTokens = 0
	}
	return &Manager{
		store:             store,
		compactionTurns:   compactionTurns,
		compactionTokens:  compactionTokens,
		keepRecentTurns:   keep,
		summaryMaxRunes:   8000,
		pinnedFactsMaxLen: 128,
	}
}

func (m *Manager) CreateSession(ctx context.Context) (Session, error) {
	now := time.Now().UTC()
	id := buildID("sess", now.String())
	s := Session{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := m.save(ctx, s); err != nil {
		return Session{}, err
	}
	return s, nil
}

func (m *Manager) GetSession(ctx context.Context, sessionID string) (Session, error) {
	return m.load(ctx, sessionID)
}

func (m *Manager) ListSessionIDs(ctx context.Context, limit int) ([]string, error) {
	return m.store.ListSessionIDs(ctx, limit)
}

func (m *Manager) AppendTurn(ctx context.Context, sessionID string, turn message.Turn) (Session, error) {
	s, err := m.load(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	s.Turns = append(s.Turns, turn)
	s.UpdatedAt = time.Now().UTC()
	m.maybeCompact(&s)
	if err := m.save(ctx, s); err != nil {
		return Session{}, err
	}
	return s, nil
}

func (m *Manager) UpdateActiveRoot(ctx context.Context, sessionID, activeRoot string) (Session, error) {
	s, err := m.load(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	next := normalizeActiveRoot(activeRoot)
	if s.ActiveRoot == next {
		return s, nil
	}
	s.ActiveRoot = next
	s.UpdatedAt = time.Now().UTC()
	if err := m.save(ctx, s); err != nil {
		return Session{}, err
	}
	return s, nil
}

func (m *Manager) CreateApproval(ctx context.Context, sessionID, turnID, toolName, args string) (Approval, error) {
	now := time.Now().UTC()
	a := Approval{
		ID:        buildID("apr", sessionID+turnID+toolName+args+now.String()),
		SessionID: sessionID,
		TurnID:    turnID,
		ToolName:  toolName,
		Args:      args,
		Status:    ApprovalPending,
		CreatedAt: now,
	}
	if err := m.saveApproval(ctx, a); err != nil {
		return Approval{}, err
	}
	return a, nil
}

func (m *Manager) GetApproval(ctx context.Context, approvalID string) (Approval, error) {
	raw, err := m.store.LoadApproval(ctx, strings.TrimSpace(approvalID))
	if err != nil {
		return Approval{}, err
	}
	var a Approval
	if err := json.Unmarshal(raw, &a); err != nil {
		return Approval{}, fmt.Errorf("decode approval: %w", err)
	}
	return a, nil
}

func (m *Manager) ResolveApproval(ctx context.Context, approvalID string, approved bool) (Approval, error) {
	a, err := m.GetApproval(ctx, approvalID)
	if err != nil {
		return Approval{}, err
	}
	if a.Status != ApprovalPending {
		return a, nil
	}
	if approved {
		a.Status = ApprovalApproved
	} else {
		a.Status = ApprovalRejected
	}
	a.ResolvedAt = time.Now().UTC()
	if err := m.saveApproval(ctx, a); err != nil {
		return Approval{}, err
	}
	return a, nil
}

func (m *Manager) load(ctx context.Context, sessionID string) (Session, error) {
	raw, err := m.store.LoadSession(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return Session{}, err
	}
	var s Session
	if err := json.Unmarshal(raw, &s); err != nil {
		return Session{}, fmt.Errorf("decode session: %w", err)
	}
	return s, nil
}

func (m *Manager) save(ctx context.Context, s Session) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	return m.store.SaveSession(ctx, s.ID, raw)
}

func (m *Manager) saveApproval(ctx context.Context, a Approval) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encode approval: %w", err)
	}
	return m.store.SaveApproval(ctx, a.ID, raw)
}

func (m *Manager) maybeCompact(s *Session) {
	turnTrigger := len(s.Turns) > m.compactionTurns
	tokenTrigger := false
	if m.compactionTokens > 0 {
		tokenTrigger = estimateSessionTokens(*s) > m.compactionTokens
	}
	if !turnTrigger && !tokenTrigger {
		return
	}
	keepRecent := m.keepRecentTurns
	if len(s.Turns) <= keepRecent {
		if tokenTrigger && len(s.Turns) > 2 {
			keepRecent = len(s.Turns) / 2
			if keepRecent < 2 {
				keepRecent = 2
			}
		} else {
			return
		}
	}
	oldTurns := append([]message.Turn(nil), s.Turns[:len(s.Turns)-keepRecent]...)
	s.ArchivedTurns = append(s.ArchivedTurns, oldTurns...)
	s.Turns = append([]message.Turn(nil), s.Turns[len(s.Turns)-keepRecent:]...)
	s.WorkingSummary = mergeSummary(s.WorkingSummary, summarizeTurns(oldTurns), m.summaryMaxRunes)
	s.PinnedFacts = mergePinnedFacts(s.PinnedFacts, extractPinnedFacts(oldTurns), m.pinnedFactsMaxLen)
}

func summarizeTurns(turns []message.Turn) string {
	if len(turns) == 0 {
		return ""
	}
	var b strings.Builder
	for _, t := range turns {
		if strings.TrimSpace(t.UserInput) != "" {
			b.WriteString("- user: ")
			b.WriteString(singleLine(t.UserInput, 160))
			b.WriteString("\n")
		}
		for _, m := range t.Messages {
			if m.Role == message.RoleAssistant && strings.TrimSpace(m.Content) != "" {
				b.WriteString("  assistant: ")
				b.WriteString(singleLine(m.Content, 220))
				b.WriteString("\n")
				break
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func extractPinnedFacts(turns []message.Turn) []string {
	keywords := []string{"must", "should", "require", "constraint", "forbid", "required", "rule", "need"}
	var out []string
	for _, t := range turns {
		for _, m := range t.Messages {
			content := strings.TrimSpace(m.Content)
			if content == "" {
				continue
			}
			lower := strings.ToLower(content)
			for _, kw := range keywords {
				if strings.Contains(lower, strings.ToLower(kw)) {
					out = append(out, singleLine(content, 220))
					break
				}
			}
		}
	}
	return dedupe(out)
}

func mergeSummary(oldSummary, extra string, maxRunes int) string {
	oldSummary = strings.TrimSpace(oldSummary)
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return oldSummary
	}
	if oldSummary == "" {
		return clip(extra, maxRunes)
	}
	return clip(oldSummary+"\n"+extra, maxRunes)
}

func mergePinnedFacts(oldFacts, extra []string, max int) []string {
	out := append([]string(nil), oldFacts...)
	for _, f := range extra {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
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

func clip(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func singleLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	s = strings.TrimSpace(strings.ReplaceAll(s, "\t", " "))
	return clip(s, max)
}

func estimateSessionTokens(s Session) int {
	var totalRunes int
	totalRunes += len([]rune(s.WorkingSummary))
	for _, f := range s.PinnedFacts {
		totalRunes += len([]rune(f))
	}
	for _, t := range s.Turns {
		totalRunes += len([]rune(t.UserInput))
		for _, m := range t.Messages {
			totalRunes += len([]rune(m.Content))
		}
	}
	if totalRunes <= 0 {
		return 0
	}
	// Approximation: mixed-language prompts are usually within 1 token per 3-4 chars.
	return (totalRunes + 3) / 4
}

func normalizeActiveRoot(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
	}
	if path == "." {
		return ""
	}
	return path
}

func buildID(prefix, seed string) string {
	sum := sha1.Sum([]byte(seed))
	return prefix + "_" + hex.EncodeToString(sum[:8])
}
