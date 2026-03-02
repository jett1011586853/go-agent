package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	yaml "go.yaml.in/yaml/v3"
)

const skillAlignmentPreamble = `Skill alignment rules for this runtime:
- The executing model is GLM5 in go-agent (not Claude).
- Any mention of "Claude" in skill text refers to the current agent.
- Always obey go-agent runtime constraints: JSON action loop, enabled tools, permission engine, workspace root limits, and safety rules.
- If a skill instruction conflicts with runtime/tool constraints, keep the intent but follow runtime constraints.`

type Embedder interface {
	Embed(ctx context.Context, texts []string, inputType string) ([][]float64, error)
}

type Reranker interface {
	Rerank(ctx context.Context, query string, docs []string, topN int) ([]int, error)
}

type Skill struct {
	Name        string
	Description string
	Path        string
	Body        string
	UseWhen     []string
	AvoidWhen   []string
	Required    []SkillArg
	Examples    []SkillExample
}

type SkillArg struct {
	Name        string
	Description string
	Required    bool
}

type SkillExample struct {
	Input    string
	Decision string
	Output   string
}

type Summary struct {
	Name        string
	Description string
}

type Options struct {
	Dirs            []string
	Bootstrap       string
	AutoLoad        bool
	AutoLoadK       int
	CandidateK      int
	MaxContextChars int
}

type ContextResult struct {
	Text   string
	Loaded []Summary
}

type Service struct {
	opts Options

	mu      sync.RWMutex
	skills  []Skill
	byName  map[string]Skill
	vectors map[string][]float64

	embedder Embedder
	reranker Reranker
}

func NewService(opts Options) *Service {
	o := normalizeOptions(opts)
	return &Service{
		opts:    o,
		byName:  map[string]Skill{},
		vectors: map[string][]float64{},
	}
}

func (s *Service) SetEmbedder(embedder Embedder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.embedder = embedder
}

func (s *Service) SetReranker(reranker Reranker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reranker = reranker
}

func (s *Service) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.skills)
}

func (s *Service) Load() error {
	s.mu.Lock()
	opts := s.opts
	s.mu.Unlock()

	items, err := discoverSkills(opts.Dirs)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return fmt.Errorf("no skills found in configured dirs")
	}

	byName := make(map[string]Skill, len(items))
	for _, item := range items {
		key := skillKey(item.Name)
		if key == "" {
			continue
		}
		if _, exists := byName[key]; !exists {
			byName[key] = item
			continue
		}
		// Keep both skills when names collide across different skill sets.
		// The later one gets a namespaced alias like "<source>/<name>".
		source := skillSourceAlias(item.Path)
		if source == "" {
			source = "skills"
		}
		baseName := strings.TrimSpace(item.Name)
		alias := source + "/" + baseName
		aliasKey := skillKey(alias)
		for i := 2; ; i++ {
			if _, used := byName[aliasKey]; !used {
				item.Name = alias
				byName[aliasKey] = item
				break
			}
			alias = fmt.Sprintf("%s/%s-%d", source, baseName, i)
			aliasKey = skillKey(alias)
		}
	}
	if len(byName) == 0 {
		return fmt.Errorf("no valid skills found")
	}

	ordered := make([]Skill, 0, len(byName))
	for _, item := range byName {
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return strings.ToLower(ordered[i].Name) < strings.ToLower(ordered[j].Name)
	})

	s.mu.Lock()
	s.skills = ordered
	s.byName = byName
	s.vectors = map[string][]float64{}
	s.mu.Unlock()
	return nil
}

func (s *Service) List(limit int) []Summary {
	s.mu.RLock()
	skills := append([]Skill(nil), s.skills...)
	s.mu.RUnlock()
	if limit > 0 && len(skills) > limit {
		skills = skills[:limit]
	}
	out := make([]Summary, 0, len(skills))
	for _, item := range skills {
		out = append(out, Summary{
			Name:        item.Name,
			Description: item.Description,
		})
	}
	return out
}

func (s *Service) LoadByName(name string) (Skill, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return Skill{}, errors.New("skill name is required")
	}
	s.mu.RLock()
	item, ok := s.byName[key]
	s.mu.RUnlock()
	if !ok {
		return Skill{}, fmt.Errorf("skill %q not found", name)
	}
	return item, nil
}

func (s *Service) BuildContext(ctx context.Context, query string) (ContextResult, error) {
	query = strings.TrimSpace(query)
	s.mu.RLock()
	opts := s.opts
	skills := append([]Skill(nil), s.skills...)
	byName := make(map[string]Skill, len(s.byName))
	for k, v := range s.byName {
		byName[k] = v
	}
	s.mu.RUnlock()
	if len(skills) == 0 {
		return ContextResult{}, nil
	}

	seen := map[string]struct{}{}
	selected := make([]Skill, 0, opts.AutoLoadK+1)

	bootstrapKey := strings.ToLower(strings.TrimSpace(opts.Bootstrap))
	if bootstrapKey != "" {
		if item, ok := byName[bootstrapKey]; ok {
			selected = append(selected, item)
			seen[bootstrapKey] = struct{}{}
		}
	}

	if opts.AutoLoad && query != "" && opts.AutoLoadK > 0 {
		auto := s.selectRelevant(ctx, query, opts.AutoLoadK, opts.CandidateK, bootstrapKey)
		for _, item := range auto {
			key := strings.ToLower(strings.TrimSpace(item.Name))
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			selected = append(selected, item)
		}
	}

	if len(selected) == 0 {
		return ContextResult{}, nil
	}

	var b strings.Builder
	b.WriteString("Loaded local workflow skills (SKILL.md):\n")
	b.WriteString(skillAlignmentPreamble)
	b.WriteString("\n\n")
	used := 0
	loaded := make([]Summary, 0, len(selected))
	for i, item := range selected {
		desc := strings.TrimSpace(item.Description)
		if desc == "" {
			desc = "(no description)"
		}
		meta := buildSkillMetadataBlock(item)
		if meta != "" {
			meta += "\n\n"
		}
		block := fmt.Sprintf(
			"[SKILL %d] %s\nsource: %s\ndescription: %s\n%s%s\n\n",
			i+1,
			item.Name,
			filepath.ToSlash(item.Path),
			desc,
			meta,
			adaptSkillBody(item.Body),
		)
		blockRunes := utf8.RuneCountInString(block)
		if opts.MaxContextChars > 0 && used+blockRunes > opts.MaxContextChars {
			remain := opts.MaxContextChars - used
			if remain <= 0 {
				break
			}
			b.WriteString(clipRunes(block, remain))
			loaded = append(loaded, Summary{Name: item.Name, Description: item.Description})
			break
		}
		b.WriteString(block)
		used += blockRunes
		loaded = append(loaded, Summary{Name: item.Name, Description: item.Description})
	}

	return ContextResult{
		Text:   strings.TrimSpace(b.String()),
		Loaded: loaded,
	}, nil
}

func (s *Service) selectRelevant(ctx context.Context, query string, topK, candidateK int, excludeName string) []Skill {
	query = strings.TrimSpace(query)
	if query == "" || topK <= 0 {
		return nil
	}

	s.mu.RLock()
	skills := append([]Skill(nil), s.skills...)
	embedder := s.embedder
	reranker := s.reranker
	s.mu.RUnlock()
	if len(skills) == 0 {
		return nil
	}

	type rankedSkill struct {
		skill Skill
		score float64
	}
	ranked := make([]rankedSkill, 0, len(skills))
	for _, item := range skills {
		if strings.EqualFold(strings.TrimSpace(item.Name), excludeName) {
			continue
		}
		lex := lexicalScore(query, skillSearchText(item))
		ranked = append(ranked, rankedSkill{
			skill: item,
			score: lex,
		})
	}
	if len(ranked) == 0 {
		return nil
	}

	if embedder != nil {
		if qVecs, err := embedder.Embed(ctx, []string{query}, "query"); err == nil && len(qVecs) > 0 && len(qVecs[0]) > 0 {
			vecs := s.ensureSkillVectors(ctx, embedder, skills)
			if len(vecs) > 0 {
				for i := range ranked {
					key := strings.ToLower(strings.TrimSpace(ranked[i].skill.Name))
					if vec, ok := vecs[key]; ok && len(vec) > 0 {
						embedScore := cosine(qVecs[0], vec)
						ranked[i].score = embedScore + (0.1 * ranked[i].score)
					}
				}
			}
		}
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return strings.ToLower(ranked[i].skill.Name) < strings.ToLower(ranked[j].skill.Name)
		}
		return ranked[i].score > ranked[j].score
	})

	if candidateK <= 0 || candidateK > len(ranked) {
		candidateK = len(ranked)
	}
	candidates := ranked[:candidateK]
	if reranker != nil && len(candidates) > 1 {
		docs := make([]string, 0, len(candidates))
		for _, c := range candidates {
			docs = append(docs, c.skill.Name+"\n"+c.skill.Description)
		}
		if order, err := reranker.Rerank(ctx, query, docs, topK); err == nil && len(order) > 0 {
			reordered := make([]rankedSkill, 0, len(candidates))
			used := map[int]struct{}{}
			for _, idx := range order {
				if idx < 0 || idx >= len(candidates) {
					continue
				}
				if _, ok := used[idx]; ok {
					continue
				}
				used[idx] = struct{}{}
				reordered = append(reordered, candidates[idx])
			}
			for idx := range candidates {
				if _, ok := used[idx]; ok {
					continue
				}
				reordered = append(reordered, candidates[idx])
			}
			candidates = reordered
		}
	}

	if topK > len(candidates) {
		topK = len(candidates)
	}
	out := make([]Skill, 0, topK)
	for i := 0; i < topK; i++ {
		out = append(out, candidates[i].skill)
	}
	return out
}

func (s *Service) ensureSkillVectors(ctx context.Context, embedder Embedder, skills []Skill) map[string][]float64 {
	s.mu.RLock()
	if len(s.vectors) == len(s.skills) && len(s.vectors) > 0 {
		out := make(map[string][]float64, len(s.vectors))
		for k, v := range s.vectors {
			out[k] = append([]float64(nil), v...)
		}
		s.mu.RUnlock()
		return out
	}
	s.mu.RUnlock()

	texts := make([]string, 0, len(skills))
	keys := make([]string, 0, len(skills))
	for _, item := range skills {
		key := strings.ToLower(strings.TrimSpace(item.Name))
		if key == "" {
			continue
		}
		keys = append(keys, key)
		texts = append(texts, skillSearchText(item))
	}
	if len(texts) == 0 {
		return nil
	}
	vecs, err := embedder.Embed(ctx, texts, "passage")
	if err != nil || len(vecs) != len(keys) {
		return nil
	}
	built := make(map[string][]float64, len(keys))
	for i, key := range keys {
		if len(vecs[i]) == 0 {
			continue
		}
		built[key] = append([]float64(nil), vecs[i]...)
	}
	if len(built) == 0 {
		return nil
	}

	s.mu.Lock()
	s.vectors = built
	s.mu.Unlock()
	out := make(map[string][]float64, len(built))
	for k, v := range built {
		out[k] = append([]float64(nil), v...)
	}
	return out
}

func discoverSkills(dirs []string) ([]Skill, error) {
	seenPath := map[string]struct{}{}
	out := make([]Skill, 0, 64)
	foundAnyDir := false
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		info, err := os.Stat(absDir)
		if err != nil || !info.IsDir() {
			continue
		}
		foundAnyDir = true
		_ = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d == nil || d.IsDir() {
				return nil
			}
			absPath, err := filepath.Abs(path)
			if err != nil {
				return nil
			}
			if _, dup := seenPath[absPath]; dup {
				return nil
			}

			var item Skill
			if strings.EqualFold(d.Name(), "SKILL.md") {
				item, err = parseSkillFile(absPath)
				if err != nil {
					return nil
				}
			} else if isWorkflowMarkdown(absPath) {
				item, err = parseWorkflowFile(absPath)
				if err != nil {
					return nil
				}
			} else {
				return nil
			}

			seenPath[absPath] = struct{}{}
			out = append(out, item)
			return nil
		})
	}
	if !foundAnyDir {
		return nil, fmt.Errorf("skills dirs not found")
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func isWorkflowMarkdown(path string) bool {
	if !strings.EqualFold(filepath.Ext(path), ".md") {
		return false
	}
	parent := filepath.Base(filepath.Dir(path))
	return strings.EqualFold(parent, "workflows")
}

type skillFrontmatter struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	UseWhen      any    `yaml:"use_when"`
	AvoidWhen    any    `yaml:"avoid_when"`
	RequiredArgs any    `yaml:"required_args"`
	Examples     any    `yaml:"examples"`
}

func parseSkillFile(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	fm, body := parseFrontmatter(text)
	if strings.TrimSpace(fm.Name) == "" {
		fm.Name = filepath.Base(filepath.Dir(path))
	}
	if strings.TrimSpace(body) == "" {
		body = strings.TrimSpace(text)
	}
	return Skill{
		Name:        strings.TrimSpace(fm.Name),
		Description: strings.TrimSpace(fm.Description),
		Path:        path,
		Body:        strings.TrimSpace(body),
		UseWhen:     normalizeStringList(anyToStringSlice(fm.UseWhen)),
		AvoidWhen:   normalizeStringList(anyToStringSlice(fm.AvoidWhen)),
		Required:    parseRequiredArgs(fm.RequiredArgs),
		Examples:    parseExamples(fm.Examples),
	}, nil
}

func parseWorkflowFile(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	text := strings.TrimSpace(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if text == "" {
		return Skill{}, fmt.Errorf("empty workflow file: %s", path)
	}
	base := strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if base == "" {
		return Skill{}, fmt.Errorf("invalid workflow filename: %s", path)
	}
	desc := extractPurpose(text)
	if desc == "" {
		desc = "GSD workflow command /gsd:" + base
	}
	return Skill{
		Name:        "gsd:" + base,
		Description: desc,
		Path:        path,
		Body:        text,
		UseWhen: []string{
			"Use when the user asks for /gsd:" + base + " workflow behavior.",
		},
	}, nil
}

func extractPurpose(text string) string {
	lower := strings.ToLower(text)
	start := strings.Index(lower, "<purpose>")
	end := strings.Index(lower, "</purpose>")
	if start == -1 || end == -1 || end <= start {
		return ""
	}
	block := strings.TrimSpace(text[start+len("<purpose>") : end])
	block = strings.Join(strings.Fields(block), " ")
	if block == "" {
		return ""
	}
	if len(block) > 220 {
		return strings.TrimSpace(block[:220]) + "..."
	}
	return block
}

func parseFrontmatter(text string) (skillFrontmatter, string) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return skillFrontmatter{}, ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return skillFrontmatter{}, strings.TrimSpace(text)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end <= 1 {
		return skillFrontmatter{}, strings.TrimSpace(text)
	}
	fmRaw := strings.Join(lines[1:end], "\n")
	bodyRaw := strings.Join(lines[end+1:], "\n")
	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(fmRaw), &fm); err != nil {
		return skillFrontmatter{}, strings.TrimSpace(text)
	}
	return fm, strings.TrimSpace(bodyRaw)
}

func anyToStringSlice(v any) []string {
	switch vv := v.(type) {
	case nil:
		return nil
	case string:
		s := strings.TrimSpace(vv)
		if s == "" {
			return nil
		}
		return []string{s}
	case []any:
		out := make([]string, 0, len(vv))
		for _, item := range vv {
			switch it := item.(type) {
			case string:
				s := strings.TrimSpace(it)
				if s != "" {
					out = append(out, s)
				}
			case map[string]any:
				if w := strings.TrimSpace(anyToString(it["when"])); w != "" {
					out = append(out, w)
					continue
				}
				if d := strings.TrimSpace(anyToString(it["description"])); d != "" {
					out = append(out, d)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func parseRequiredArgs(v any) []SkillArg {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]SkillArg, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(anyToString(m["name"]))
		if name == "" {
			continue
		}
		out = append(out, SkillArg{
			Name:        name,
			Description: strings.TrimSpace(anyToString(m["description"])),
			Required:    anyToBool(m["required"]),
		})
	}
	return out
}

func parseExamples(v any) []SkillExample {
	switch vv := v.(type) {
	case string:
		s := strings.TrimSpace(vv)
		if s == "" {
			return nil
		}
		return []SkillExample{{Input: s}}
	case []any:
		out := make([]SkillExample, 0, len(vv))
		for _, item := range vv {
			switch it := item.(type) {
			case string:
				s := strings.TrimSpace(it)
				if s != "" {
					out = append(out, SkillExample{Input: s})
				}
			case map[string]any:
				in := strings.TrimSpace(anyToString(it["input"]))
				if in == "" {
					in = strings.TrimSpace(anyToString(it["user"]))
				}
				decision := strings.TrimSpace(anyToString(it["decision"]))
				if decision == "" {
					decision = strings.TrimSpace(anyToString(it["action"]))
				}
				outp := strings.TrimSpace(anyToString(it["output"]))
				if in == "" && decision == "" && outp == "" {
					continue
				}
				out = append(out, SkillExample{
					Input:    in,
					Decision: decision,
					Output:   outp,
				})
			}
		}
		return out
	default:
		return nil
	}
}

func anyToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func anyToBool(v any) bool {
	switch vv := v.(type) {
	case bool:
		return vv
	case string:
		s := strings.ToLower(strings.TrimSpace(vv))
		return s == "1" || s == "true" || s == "yes" || s == "on"
	default:
		return false
	}
}

func buildSkillMetadataBlock(item Skill) string {
	lines := make([]string, 0, 8)
	if len(item.UseWhen) > 0 {
		lines = append(lines, "use_when: "+strings.Join(limitStrings(item.UseWhen, 2), " | "))
	}
	if len(item.AvoidWhen) > 0 {
		lines = append(lines, "avoid_when: "+strings.Join(limitStrings(item.AvoidWhen, 2), " | "))
	}
	if len(item.Required) > 0 {
		parts := make([]string, 0, 3)
		for _, arg := range item.Required {
			if len(parts) >= 3 {
				break
			}
			name := strings.TrimSpace(arg.Name)
			if name == "" {
				continue
			}
			if arg.Required {
				name += " (required)"
			}
			desc := strings.TrimSpace(arg.Description)
			if desc != "" {
				name += ": " + desc
			}
			parts = append(parts, name)
		}
		if len(parts) > 0 {
			lines = append(lines, "required_args: "+strings.Join(parts, " | "))
		}
	}
	if len(item.Examples) > 0 {
		ex := item.Examples[0]
		exParts := make([]string, 0, 3)
		if s := strings.TrimSpace(ex.Input); s != "" {
			exParts = append(exParts, "input="+s)
		}
		if s := strings.TrimSpace(ex.Decision); s != "" {
			exParts = append(exParts, "decision="+s)
		}
		if s := strings.TrimSpace(ex.Output); s != "" {
			exParts = append(exParts, "output="+s)
		}
		if len(exParts) > 0 {
			lines = append(lines, "example: "+strings.Join(exParts, " ; "))
		}
	}
	return strings.Join(lines, "\n")
}

func limitStrings(items []string, n int) []string {
	if n <= 0 || len(items) <= n {
		return items
	}
	return items[:n]
}

func skillSearchText(item Skill) string {
	var b strings.Builder
	b.WriteString(item.Name)
	b.WriteString("\n")
	b.WriteString(item.Description)
	if len(item.UseWhen) > 0 {
		b.WriteString("\nuse_when: ")
		b.WriteString(strings.Join(item.UseWhen, " "))
	}
	if len(item.AvoidWhen) > 0 {
		b.WriteString("\navoid_when: ")
		b.WriteString(strings.Join(item.AvoidWhen, " "))
	}
	for _, arg := range item.Required {
		name := strings.TrimSpace(arg.Name)
		if name == "" {
			continue
		}
		b.WriteString("\narg ")
		b.WriteString(name)
		b.WriteString(" ")
		b.WriteString(strings.TrimSpace(arg.Description))
	}
	return b.String()
}

func normalizeOptions(opts Options) Options {
	opts.Dirs = normalizeStringList(opts.Dirs)
	if opts.AutoLoadK <= 0 {
		opts.AutoLoadK = 2
	}
	if opts.CandidateK <= 0 {
		opts.CandidateK = 12
	}
	if opts.CandidateK < opts.AutoLoadK {
		opts.CandidateK = opts.AutoLoadK
	}
	if opts.MaxContextChars <= 0 {
		opts.MaxContextChars = 12000
	}
	return opts
}

func normalizeStringList(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func skillKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func skillSourceAlias(path string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if !strings.EqualFold(strings.TrimSpace(part), "skills") {
			continue
		}
		if i > 0 {
			if alias := sanitizeAlias(parts[i-1]); alias != "" {
				return alias
			}
		}
		break
	}
	if len(parts) >= 3 {
		if alias := sanitizeAlias(parts[len(parts)-3]); alias != "" {
			return alias
		}
	}
	return ""
}

func sanitizeAlias(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
			continue
		}
		if r == ' ' || r == '/' || r == '\\' || r == '.' {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return ""
	}
	return out
}

func lexicalScore(query, text string) float64 {
	query = strings.ToLower(strings.TrimSpace(query))
	text = strings.ToLower(strings.TrimSpace(text))
	if query == "" || text == "" {
		return 0
	}
	tokens := strings.Fields(query)
	if len(tokens) == 0 {
		return 0
	}
	var hits float64
	for _, token := range tokens {
		if token == "" {
			continue
		}
		if strings.Contains(text, token) {
			hits++
		}
	}
	return hits / float64(len(tokens))
}

func cosine(a, b []float64) float64 {
	maxLen := len(a)
	if len(b) < maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := 0; i < maxLen; i++ {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func clipRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func adaptSkillBody(body string) string {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" {
		return body
	}
	replacer := strings.NewReplacer(
		"fresh Claude", "fresh agent context",
		"Fresh Claude", "Fresh agent context",
		"Claude", "current agent",
		"claude", "current agent",
	)
	return replacer.Replace(body)
}
