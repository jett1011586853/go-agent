package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceLoadAndBuildContext(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "skills")
	mustWriteSkill(t, filepath.Join(skillsRoot, "using-superpowers", "SKILL.md"), `---
name: using-superpowers
description: Bootstrap skill. Load skills before acting.
---
Always consider skills first. Ask fresh Claude to review.`)
	mustWriteSkill(t, filepath.Join(skillsRoot, "writing-plans", "SKILL.md"), `---
name: writing-plans
description: Use when the task needs a concrete implementation plan.
---
Create a phased implementation plan with milestones.`)
	mustWriteSkill(t, filepath.Join(skillsRoot, "brainstorming", "SKILL.md"), `---
name: brainstorming
description: Use when requirements are unclear and ideas are needed.
---
Generate candidate directions and tradeoffs.`)

	svc := NewService(Options{
		Dirs:            []string{skillsRoot},
		Bootstrap:       "using-superpowers",
		AutoLoad:        true,
		AutoLoadK:       1,
		CandidateK:      3,
		MaxContextChars: 8000,
	})
	if err := svc.Load(); err != nil {
		t.Fatalf("load skills: %v", err)
	}
	if svc.Count() != 3 {
		t.Fatalf("unexpected skill count: %d", svc.Count())
	}

	items := svc.List(10)
	if len(items) != 3 {
		t.Fatalf("unexpected listed count: %d", len(items))
	}
	if _, err := svc.LoadByName("Writing-Plans"); err != nil {
		t.Fatalf("load by name should be case-insensitive: %v", err)
	}

	ctxOut, err := svc.BuildContext(context.Background(), "please write an implementation plan")
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if !strings.Contains(ctxOut.Text, "using-superpowers") {
		t.Fatalf("expected bootstrap skill in context, got: %s", ctxOut.Text)
	}
	if !strings.Contains(ctxOut.Text, "writing-plans") {
		t.Fatalf("expected relevant skill in context, got: %s", ctxOut.Text)
	}
	if !strings.Contains(ctxOut.Text, "Skill alignment rules for this runtime:") {
		t.Fatalf("expected alignment preamble in context")
	}
	if strings.Contains(ctxOut.Text, "fresh Claude to review") {
		t.Fatalf("expected model references inside skill body to be adapted")
	}
	if !strings.Contains(ctxOut.Text, "fresh agent context to review") {
		t.Fatalf("expected adapted skill body phrase, got: %s", ctxOut.Text)
	}
}

func TestServiceKeepsDuplicateSkillNamesAcrossDirs(t *testing.T) {
	root := t.TempDir()
	superDir := filepath.Join(root, "superpowers", "skills")
	anthropicDir := filepath.Join(root, "anthropic", "skills")
	mustWriteSkill(t, filepath.Join(superDir, "skill-creator", "SKILL.md"), `---
name: skill-creator
description: superpowers source
---
super`)
	mustWriteSkill(t, filepath.Join(anthropicDir, "skill-creator", "SKILL.md"), `---
name: skill-creator
description: anthropic source
---
anthropic`)

	svc := NewService(Options{
		Dirs: []string{superDir, anthropicDir},
	})
	if err := svc.Load(); err != nil {
		t.Fatalf("load skills: %v", err)
	}
	items := svc.List(10)
	if len(items) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(items))
	}
	if _, err := svc.LoadByName("skill-creator"); err != nil {
		t.Fatalf("expected base skill name to exist: %v", err)
	}
	if _, err := svc.LoadByName("anthropic/skill-creator"); err != nil {
		t.Fatalf("expected namespaced duplicate to exist: %v", err)
	}
}

func TestServiceLoadsGSDWorkflows(t *testing.T) {
	root := t.TempDir()
	workflowsDir := filepath.Join(root, "gsd", "get-shit-done", "workflows")
	mustWriteSkill(t, filepath.Join(workflowsDir, "quick.md"), `<purpose>
Execute small, ad-hoc tasks with GSD guarantees.
</purpose>

<process>
Do the thing.
</process>`)

	svc := NewService(Options{
		Dirs: []string{workflowsDir},
	})
	if err := svc.Load(); err != nil {
		t.Fatalf("load gsd workflows: %v", err)
	}
	if svc.Count() != 1 {
		t.Fatalf("expected 1 workflow skill, got %d", svc.Count())
	}

	item, err := svc.LoadByName("gsd:quick")
	if err != nil {
		t.Fatalf("load workflow by name: %v", err)
	}
	if item.Name != "gsd:quick" {
		t.Fatalf("unexpected workflow name: %s", item.Name)
	}
	if !strings.Contains(item.Description, "Execute small") {
		t.Fatalf("unexpected description: %s", item.Description)
	}
}

func TestServiceParsesSkillMetadata(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "skills")
	mustWriteSkill(t, filepath.Join(skillsRoot, "weather", "SKILL.md"), `---
name: weather-tool
description: Read weather data
use_when:
  - User asks current weather
avoid_when:
  - User asks stock price
required_args:
  - name: city
    description: Chinese city name
    required: true
examples:
  - input: 北京现在天气
    decision: call get_realtime_weather
---
Use weather API.`)

	svc := NewService(Options{
		Dirs:      []string{skillsRoot},
		Bootstrap: "weather-tool",
	})
	if err := svc.Load(); err != nil {
		t.Fatalf("load skills: %v", err)
	}
	item, err := svc.LoadByName("weather-tool")
	if err != nil {
		t.Fatalf("load skill by name: %v", err)
	}
	if len(item.UseWhen) == 0 || item.UseWhen[0] != "User asks current weather" {
		t.Fatalf("unexpected use_when: %#v", item.UseWhen)
	}
	if len(item.Required) == 0 || item.Required[0].Name != "city" || !item.Required[0].Required {
		t.Fatalf("unexpected required args: %#v", item.Required)
	}
	if len(item.Examples) == 0 || !strings.Contains(item.Examples[0].Decision, "realtime") {
		t.Fatalf("unexpected examples: %#v", item.Examples)
	}

	ctxOut, err := svc.BuildContext(context.Background(), "北京天气怎么样")
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if !strings.Contains(ctxOut.Text, "use_when: User asks current weather") {
		t.Fatalf("metadata should be present in context: %s", ctxOut.Text)
	}
}

func mustWriteSkill(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
