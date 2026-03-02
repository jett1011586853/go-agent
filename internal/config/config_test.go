package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPhaseMaxFilesFromYAML(t *testing.T) {
	t.Setenv("NVIDIA_API_KEY", "test-key")
	root := t.TempDir()
	yamlPath := filepath.Join(root, "agent.yaml")
	content := "workspace_root: " + root + "\nphase_max_files: 7\n"
	if err := os.WriteFile(yamlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.PhaseMaxFiles != 7 {
		t.Fatalf("expected phase_max_files=7, got %d", cfg.PhaseMaxFiles)
	}
}

func TestLoadPhaseMaxFilesFromEnv(t *testing.T) {
	t.Setenv("NVIDIA_API_KEY", "test-key")
	t.Setenv("PHASE_MAX_FILES", "9")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.PhaseMaxFiles != 9 {
		t.Fatalf("expected phase_max_files=9 from env, got %d", cfg.PhaseMaxFiles)
	}
}

func TestLoadToolOutputSplitLimitsFromYAML(t *testing.T) {
	t.Setenv("NVIDIA_API_KEY", "test-key")
	root := t.TempDir()
	yamlPath := filepath.Join(root, "agent.yaml")
	content := "workspace_root: " + root + "\ntool_output_model_limit: 9000\ntool_output_display_limit: 28000\n"
	if err := os.WriteFile(yamlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ToolOutputModel != 9000 {
		t.Fatalf("expected tool_output_model_limit=9000, got %d", cfg.ToolOutputModel)
	}
	if cfg.ToolOutputDisplay != 28000 {
		t.Fatalf("expected tool_output_display_limit=28000, got %d", cfg.ToolOutputDisplay)
	}
	if cfg.ToolOutputLimit != 9000 {
		t.Fatalf("expected legacy tool_output_limit mirror=9000, got %d", cfg.ToolOutputLimit)
	}
}

func TestLoadLegacyToolOutputLimitMapsToBoth(t *testing.T) {
	t.Setenv("NVIDIA_API_KEY", "test-key")
	root := t.TempDir()
	yamlPath := filepath.Join(root, "agent.yaml")
	content := "workspace_root: " + root + "\ntool_output_limit: 7000\n"
	if err := os.WriteFile(yamlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ToolOutputModel != 7000 || cfg.ToolOutputDisplay != 7000 {
		t.Fatalf("expected legacy limit to set both model/display to 7000, got model=%d display=%d", cfg.ToolOutputModel, cfg.ToolOutputDisplay)
	}
}

func TestEnvLegacyToolOutputLimitDoesNotOverrideSplitFromYAML(t *testing.T) {
	t.Setenv("NVIDIA_API_KEY", "test-key")
	t.Setenv("TOOL_OUTPUT_LIMIT", "50000")
	root := t.TempDir()
	yamlPath := filepath.Join(root, "agent.yaml")
	content := "workspace_root: " + root + "\ntool_output_model_limit: 9000\ntool_output_display_limit: 28000\n"
	if err := os.WriteFile(yamlPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ToolOutputModel != 9000 {
		t.Fatalf("expected yaml tool_output_model_limit=9000, got %d", cfg.ToolOutputModel)
	}
	if cfg.ToolOutputDisplay != 28000 {
		t.Fatalf("expected yaml tool_output_display_limit=28000, got %d", cfg.ToolOutputDisplay)
	}
}

func TestEnvLegacyToolOutputLimitStillWorksWithDefaults(t *testing.T) {
	t.Setenv("NVIDIA_API_KEY", "test-key")
	t.Setenv("TOOL_OUTPUT_LIMIT", "7000")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ToolOutputModel != 7000 || cfg.ToolOutputDisplay != 7000 {
		t.Fatalf("expected legacy env limit to set both model/display to 7000, got model=%d display=%d", cfg.ToolOutputModel, cfg.ToolOutputDisplay)
	}
}
