package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-agent/internal/skills"
)

func TestSkillToolListAndLoad(t *testing.T) {
	root := t.TempDir()
	skillsRoot := filepath.Join(root, "skills")
	mustWriteSkillFile(t, filepath.Join(skillsRoot, "using-superpowers", "SKILL.md"), `---
name: using-superpowers
description: bootstrap
---
bootstrap body`)
	mustWriteSkillFile(t, filepath.Join(skillsRoot, "writing-plans", "SKILL.md"), `---
name: writing-plans
description: planning workflow
use_when:
  - user asks for implementation roadmap
---
plan body`)

	svc := skills.NewService(skills.Options{
		Dirs:      []string{skillsRoot},
		Bootstrap: "using-superpowers",
	})
	if err := svc.Load(); err != nil {
		t.Fatalf("load skills: %v", err)
	}

	reg := NewRegistry()
	if err := RegisterSkillTool(reg, svc, 4096); err != nil {
		t.Fatalf("register skill tool: %v", err)
	}

	listRes, err := reg.Run(context.Background(), "skill", json.RawMessage(`{"action":"list","limit":10}`))
	if err != nil {
		t.Fatalf("list run failed: %v", err)
	}
	if !strings.Contains(listRes.Output, "writing-plans") {
		t.Fatalf("list output missing expected skill: %s", listRes.Output)
	}

	loadRes, err := reg.Run(context.Background(), "skill", json.RawMessage(`{"action":"load","name":"writing-plans"}`))
	if err != nil {
		t.Fatalf("load run failed: %v", err)
	}
	if !strings.Contains(loadRes.Output, "skill: writing-plans") {
		t.Fatalf("load output missing header: %s", loadRes.Output)
	}
	if !strings.Contains(loadRes.Output, "plan body") {
		t.Fatalf("load output missing body: %s", loadRes.Output)
	}
	if !strings.Contains(loadRes.Output, "use_when: user asks for implementation roadmap") {
		t.Fatalf("load output missing metadata: %s", loadRes.Output)
	}
}

func mustWriteSkillFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
