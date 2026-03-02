package app

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go-agent/internal/message"
)

type phaseController struct {
	enabled             bool
	total               int
	current             int
	maxFilesPerPhase    int
	pendingProjectCheck bool
	stage               phaseStage
	seen                map[string]struct{}
	orderedFiles        []string
}

type phaseStage string

const (
	phaseStagePlan      phaseStage = "plan"
	phaseStageImplement phaseStage = "implement"
	phaseStageVerify    phaseStage = "verify"
	phaseStageSummary   phaseStage = "summary"
	phaseStageDone      phaseStage = "done"
)

var (
	rePhaseCountCN = regexp.MustCompile(`(?i)(\d+)\s*个?\s*阶段`)
	rePhaseCountEN = regexp.MustCompile(`(?i)\b(\d+)\s*phases?\b`)
	rePhaseMarker  = regexp.MustCompile(`(?i)阶段\s*(\d+)`)
)

func newPhaseController(input string, maxFilesPerPhase int) phaseController {
	count := detectRequestedPhaseCount(input)
	if count <= 0 {
		return phaseController{}
	}
	if maxFilesPerPhase <= 0 {
		maxFilesPerPhase = 5
	}
	return phaseController{
		enabled:          true,
		total:            count,
		current:          1,
		maxFilesPerPhase: maxFilesPerPhase,
		stage:            phaseStagePlan,
		seen:             map[string]struct{}{},
	}
}

func detectRequestedPhaseCount(input string) int {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0
	}
	if m := rePhaseCountCN.FindStringSubmatch(input); len(m) > 1 {
		if n := parsePositiveInt(m[1]); n > 0 {
			return min(n, 32)
		}
	}
	if m := rePhaseCountEN.FindStringSubmatch(input); len(m) > 1 {
		if n := parsePositiveInt(m[1]); n > 0 {
			return min(n, 32)
		}
	}
	maxPhase := 0
	for _, m := range rePhaseMarker.FindAllStringSubmatch(input, -1) {
		if len(m) < 2 {
			continue
		}
		n := parsePositiveInt(m[1])
		if n > maxPhase {
			maxPhase = n
		}
	}
	if maxPhase > 0 {
		return min(maxPhase, 32)
	}
	return 0
}

func (p *phaseController) systemHint() string {
	if !p.enabled {
		return ""
	}
	files := "(none)"
	if len(p.orderedFiles) > 0 {
		show := p.orderedFiles
		if len(show) > 5 {
			show = show[:5]
		}
		files = strings.Join(show, ", ")
		if len(p.orderedFiles) > len(show) {
			files += fmt.Sprintf(" (+%d more)", len(p.orderedFiles)-len(show))
		}
	}
	pending := "no"
	if p.pendingProjectCheck {
		pending = "yes"
	}
	return fmt.Sprintf(
		"Phase governance:\n- Declared phases: %d\n- Current phase: %d/%d\n- Stage: %s\n- Touched files this phase: %d/%d\n- Pending project verify: %s\n- Current file set: %s\n- Hard rules: each phase <= %d core files; run verify(scope=\"project\") before moving to next phase/final answer; after project verify, append phase summary to docs/decision-log.md; do not finalize before all phases complete.",
		p.total,
		p.current,
		p.total,
		p.stageOrDefault(),
		len(p.orderedFiles),
		p.maxFilesPerPhase,
		pending,
		files,
		p.maxFilesPerPhase,
	)
}

func (p *phaseController) hasRemainingPhases() bool {
	return p.enabled && p.current <= p.total
}

func (p *phaseController) changedFiles() []string {
	if len(p.orderedFiles) == 0 {
		return nil
	}
	out := make([]string, len(p.orderedFiles))
	copy(out, p.orderedFiles)
	return out
}

func (p *phaseController) addWrites(writes []message.WriteRecord) error {
	if !p.enabled || len(writes) == 0 {
		return nil
	}
	for _, w := range writes {
		op := strings.ToLower(strings.TrimSpace(w.Op))
		if op == "mkdir" {
			continue
		}
		path := normalizeRelPath(w.Path)
		if path == "" {
			continue
		}
		if _, ok := p.seen[path]; ok {
			continue
		}
		p.seen[path] = struct{}{}
		p.orderedFiles = append(p.orderedFiles, path)
		if len(p.orderedFiles) > p.maxFilesPerPhase {
			overflow := append([]string(nil), p.orderedFiles...)
			sort.Strings(overflow)
			return fmt.Errorf(
				"phase file budget exceeded: touched %d files (limit=%d). split into smaller phase batches; files=%s",
				len(p.orderedFiles),
				p.maxFilesPerPhase,
				strings.Join(overflow, ", "),
			)
		}
	}
	if len(p.orderedFiles) > 0 {
		p.pendingProjectCheck = true
		p.stage = phaseStageVerify
	}
	return nil
}

func (p *phaseController) closeCurrentPhase() {
	if !p.enabled {
		return
	}
	p.current++
	p.pendingProjectCheck = false
	p.seen = map[string]struct{}{}
	p.orderedFiles = nil
	if p.current > p.total {
		p.stage = phaseStageDone
		return
	}
	p.stage = phaseStagePlan
}

func (p *phaseController) setStage(next phaseStage) bool {
	if !p.enabled {
		return false
	}
	if next == "" {
		return false
	}
	if p.stage == next {
		return false
	}
	p.stage = next
	return true
}

func (p *phaseController) stageOrDefault() phaseStage {
	if p.stage != "" {
		return p.stage
	}
	if !p.enabled {
		return phaseStageDone
	}
	return phaseStagePlan
}

func inferDecisionLogPathFromWrites(paths []string) string {
	for _, p := range paths {
		rel := normalizeRelPath(p)
		if strings.HasSuffix(strings.ToLower(rel), "docs/decision-log.md") {
			return rel
		}
	}
	if len(paths) == 0 {
		return "docs/decision-log.md"
	}
	first := normalizeRelPath(paths[0])
	parts := strings.Split(first, "/")
	if len(parts) >= 2 && strings.EqualFold(parts[0], "projects") {
		return filepath.ToSlash(filepath.Join(parts[0], parts[1], "docs", "decision-log.md"))
	}
	return "docs/decision-log.md"
}

func normalizeRelPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
	}
	return path
}

func parsePositiveInt(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
