package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

type AgentConfig struct {
	Mode        string            `yaml:"mode" json:"mode"`
	Model       string            `yaml:"model" json:"model"`
	Tools       []string          `yaml:"tools" json:"tools"`
	Permissions map[string]string `yaml:"permissions" json:"permissions"`
	Temperature float64           `yaml:"temperature" json:"temperature"`
	MaxTokens   int               `yaml:"max_tokens" json:"max_tokens"`
}

type fileConfig struct {
	Agents            map[string]AgentConfig `yaml:"agents"`
	DefaultAgent      string                 `yaml:"default_agent"`
	WorkspaceRoot     string                 `yaml:"workspace_root"`
	RulesFile         string                 `yaml:"rules_file"`
	StoragePath       string                 `yaml:"storage_path"`
	EmbeddingEnabled  *bool                  `yaml:"embedding_enabled"`
	EmbeddingBaseURL  string                 `yaml:"embedding_base_url"`
	EmbeddingModel    string                 `yaml:"embedding_model"`
	EmbeddingIndex    string                 `yaml:"embedding_index_path"`
	EmbeddingTopK     int                    `yaml:"embedding_top_k"`
	EmbeddingPerFile  int                    `yaml:"embedding_per_file_max"`
	EmbeddingChunk    int                    `yaml:"embedding_chunk_lines"`
	EmbeddingOverlap  *int                   `yaml:"embedding_chunk_overlap"`
	EmbeddingBatch    int                    `yaml:"embedding_batch_size"`
	EmbeddingMaxCtx   int                    `yaml:"embedding_max_context_chars"`
	EmbeddingIgnore   []string               `yaml:"embedding_ignore_dirs"`
	EmbeddingInitTime string                 `yaml:"embedding_init_timeout"`
	RerankEnabled     *bool                  `yaml:"rerank_enabled"`
	RerankBaseURL     string                 `yaml:"rerank_base_url"`
	RerankModel       string                 `yaml:"rerank_model"`
	RerankCandidateK  int                    `yaml:"rerank_candidate_k"`
	SkillsEnabled     *bool                  `yaml:"skills_enabled"`
	SkillsDirs        []string               `yaml:"skills_dirs"`
	SkillsBootstrap   string                 `yaml:"skills_bootstrap"`
	SkillsAutoLoad    *bool                  `yaml:"skills_auto_load"`
	SkillsAutoLoadK   int                    `yaml:"skills_auto_load_k"`
	SkillsCandidateK  int                    `yaml:"skills_candidate_k"`
	SkillsMaxCtx      int                    `yaml:"skills_max_context_chars"`
	CompactionTurns   int                    `yaml:"compaction_turns"`
	CompactionTokens  int                    `yaml:"compaction_token_threshold"`
	ContextTurnWindow int                    `yaml:"context_turn_window"`
	PhaseMaxFiles     int                    `yaml:"phase_max_files"`
	ToolOutputLimit   int                    `yaml:"tool_output_limit"` // legacy alias
	ToolOutputModel   int                    `yaml:"tool_output_model_limit"`
	ToolOutputDisplay int                    `yaml:"tool_output_display_limit"`
	RequestTimeout    string                 `yaml:"request_timeout"`
	TurnTimeout       string                 `yaml:"turn_timeout"`
	OneShotTimeout    string                 `yaml:"oneshot_timeout"`
	NIMBaseURL        string                 `yaml:"nim_base_url"`
	LLMMaxRetries     int                    `yaml:"llm_max_retries"`
	LLMStream         *bool                  `yaml:"llm_stream"`
	EnableThinking    *bool                  `yaml:"enable_thinking"`
	ClearThinking     *bool                  `yaml:"clear_thinking"`
	HTTPAddr          string                 `yaml:"http_addr"`
	EnableHTTP        bool                   `yaml:"enable_http"`
}

type Config struct {
	Agents            map[string]AgentConfig
	DefaultAgent      string
	WorkspaceRoot     string
	RulesFile         string
	StoragePath       string
	EmbeddingEnabled  bool
	EmbeddingBaseURL  string
	EmbeddingModel    string
	EmbeddingIndex    string
	EmbeddingTopK     int
	EmbeddingPerFile  int
	EmbeddingChunk    int
	EmbeddingOverlap  int
	EmbeddingBatch    int
	EmbeddingMaxCtx   int
	EmbeddingIgnore   []string
	EmbeddingInitTime time.Duration
	RerankEnabled     bool
	RerankBaseURL     string
	RerankModel       string
	RerankCandidateK  int
	SkillsEnabled     bool
	SkillsDirs        []string
	SkillsBootstrap   string
	SkillsAutoLoad    bool
	SkillsAutoLoadK   int
	SkillsCandidateK  int
	SkillsMaxCtx      int
	CompactionTurns   int
	CompactionTokens  int
	ContextTurnWindow int
	PhaseMaxFiles     int
	ToolOutputLimit   int // legacy alias: mirrors ToolOutputModel
	ToolOutputModel   int
	ToolOutputDisplay int
	RequestTimeout    time.Duration
	TurnTimeout       time.Duration
	OneShotTimeout    time.Duration
	NIMBaseURL        string
	NVIDIAAPIKey      string
	LLMMaxRetries     int
	LLMStream         bool
	EnableThinking    bool
	ClearThinking     bool
	HTTPAddr          string
	EnableHTTP        bool
}

func Load(configPath string) (Config, error) {
	_ = loadDotEnv(".env")
	cfg := defaultConfig()
	if strings.TrimSpace(configPath) != "" {
		if err := applyYAMLConfig(&cfg, configPath); err != nil {
			return Config{}, err
		}
	}
	applyEnvOverrides(&cfg)
	if err := normalizeAndValidate(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func defaultConfig() Config {
	cwd, _ := os.Getwd()
	workspace := cwd
	return Config{
		Agents: map[string]AgentConfig{
			"build": {
				Model: "z-ai/glm5",
				Mode:  "primary",
				Tools: []string{"list", "glob", "grep", "read", "mkdir", "write_file", "edit", "patch", "apply_diff", "verify", "bash", "webfetch", "websearch", "skill"},
				Permissions: map[string]string{
					"list":       "allow",
					"glob":       "allow",
					"grep":       "allow",
					"read":       "allow",
					"skill":      "allow",
					"verify":     "allow",
					"mkdir":      "ask",
					"write_file": "ask",
					"edit":       "ask",
					"patch":      "ask",
					"apply_diff": "ask",
					"bash":       "ask",
					"webfetch":   "ask",
					"websearch":  "ask",
					"*":          "deny",
				},
				Temperature: 0.2,
				MaxTokens:   1800,
			},
			"plan": {
				Mode:  "primary",
				Model: "z-ai/glm5",
				Tools: []string{"list", "glob", "grep", "read", "mkdir", "write_file", "edit", "patch", "apply_diff", "verify", "bash", "webfetch", "websearch", "skill"},
				Permissions: map[string]string{
					"list":       "allow",
					"glob":       "allow",
					"grep":       "allow",
					"read":       "allow",
					"skill":      "allow",
					"verify":     "allow",
					"mkdir":      "ask",
					"write_file": "ask",
					"edit":       "ask",
					"patch":      "ask",
					"apply_diff": "ask",
					"bash":       "ask",
					"webfetch":   "ask",
					"websearch":  "ask",
					"*":          "deny",
				},
				Temperature: 0.4,
				MaxTokens:   1400,
			},
		},
		DefaultAgent:     "build",
		WorkspaceRoot:    workspace,
		RulesFile:        filepath.Join(workspace, "AGENTS.md"),
		StoragePath:      filepath.Join(workspace, "data", "agent.db"),
		EmbeddingEnabled: false,
		EmbeddingBaseURL: "http://localhost:8001",
		EmbeddingModel:   "nvidia/llama-3.2-nv-embedqa-1b-v2",
		EmbeddingIndex:   filepath.Join(workspace, "data", "embed-index.jsonl"),
		EmbeddingTopK:    12,
		EmbeddingPerFile: 3,
		EmbeddingChunk:   260,
		EmbeddingOverlap: 50,
		EmbeddingBatch:   12,
		EmbeddingMaxCtx:  12000,
		EmbeddingIgnore: []string{
			".git",
			".run",
			"node_modules",
			"dist",
			"build",
			"vendor",
			"bin",
		},
		EmbeddingInitTime: 8 * time.Minute,
		RerankEnabled:     false,
		RerankBaseURL:     "http://localhost:8002",
		RerankModel:       "nvidia/llama-3.2-nv-rerankqa-1b-v2",
		RerankCandidateK:  20,
		SkillsEnabled:     false,
		SkillsDirs: []string{
			filepath.Join(workspace, "data", "superpowers", "skills"),
			filepath.Join(workspace, "data", "anthropic", "skills"),
			filepath.Join(workspace, "data", "gsd", "get-shit-done", "workflows"),
		},
		SkillsBootstrap:   "using-superpowers",
		SkillsAutoLoad:    true,
		SkillsAutoLoadK:   2,
		SkillsCandidateK:  12,
		SkillsMaxCtx:      4000,
		CompactionTurns:   30,
		CompactionTokens:  12000,
		ContextTurnWindow: 10,
		PhaseMaxFiles:     5,
		ToolOutputLimit:   12 * 1024,
		ToolOutputModel:   12 * 1024,
		ToolOutputDisplay: 50 * 1024,
		RequestTimeout:    180 * time.Second,
		TurnTimeout:       90 * time.Minute,
		OneShotTimeout:    60 * time.Minute,
		NIMBaseURL:        "https://integrate.api.nvidia.com/v1",
		NVIDIAAPIKey:      os.Getenv("NVIDIA_API_KEY"),
		LLMMaxRetries:     2,
		LLMStream:         true,
		EnableThinking:    true,
		ClearThinking:     false,
		HTTPAddr:          ":8090",
		EnableHTTP:        false,
	}
}

func applyYAMLConfig(cfg *Config, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read config file: %w", err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(raw, &fc); err != nil {
		return fmt.Errorf("parse yaml config: %w", err)
	}
	if len(fc.Agents) > 0 {
		cfg.Agents = fc.Agents
	}
	if v := strings.TrimSpace(fc.DefaultAgent); v != "" {
		cfg.DefaultAgent = v
	}
	if v := strings.TrimSpace(fc.WorkspaceRoot); v != "" {
		cfg.WorkspaceRoot = v
	}
	if v := strings.TrimSpace(fc.RulesFile); v != "" {
		cfg.RulesFile = v
	}
	if v := strings.TrimSpace(fc.StoragePath); v != "" {
		cfg.StoragePath = v
	}
	if fc.EmbeddingEnabled != nil {
		cfg.EmbeddingEnabled = *fc.EmbeddingEnabled
	}
	if v := strings.TrimSpace(fc.EmbeddingBaseURL); v != "" {
		cfg.EmbeddingBaseURL = v
	}
	if v := strings.TrimSpace(fc.EmbeddingModel); v != "" {
		cfg.EmbeddingModel = v
	}
	if v := strings.TrimSpace(fc.EmbeddingIndex); v != "" {
		cfg.EmbeddingIndex = v
	}
	if fc.EmbeddingTopK > 0 {
		cfg.EmbeddingTopK = fc.EmbeddingTopK
	}
	if fc.EmbeddingPerFile > 0 {
		cfg.EmbeddingPerFile = fc.EmbeddingPerFile
	}
	if fc.EmbeddingChunk > 0 {
		cfg.EmbeddingChunk = fc.EmbeddingChunk
	}
	if fc.EmbeddingOverlap != nil && *fc.EmbeddingOverlap >= 0 {
		cfg.EmbeddingOverlap = *fc.EmbeddingOverlap
	}
	if fc.EmbeddingBatch > 0 {
		cfg.EmbeddingBatch = fc.EmbeddingBatch
	}
	if fc.EmbeddingMaxCtx > 0 {
		cfg.EmbeddingMaxCtx = fc.EmbeddingMaxCtx
	}
	if len(fc.EmbeddingIgnore) > 0 {
		cfg.EmbeddingIgnore = append([]string(nil), fc.EmbeddingIgnore...)
	}
	if v := strings.TrimSpace(fc.EmbeddingInitTime); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid embedding_init_timeout in yaml: %w", err)
		}
		cfg.EmbeddingInitTime = d
	}
	if fc.RerankEnabled != nil {
		cfg.RerankEnabled = *fc.RerankEnabled
	}
	if v := strings.TrimSpace(fc.RerankBaseURL); v != "" {
		cfg.RerankBaseURL = v
	}
	if v := strings.TrimSpace(fc.RerankModel); v != "" {
		cfg.RerankModel = v
	}
	if fc.RerankCandidateK > 0 {
		cfg.RerankCandidateK = fc.RerankCandidateK
	}
	if fc.SkillsEnabled != nil {
		cfg.SkillsEnabled = *fc.SkillsEnabled
	}
	if len(fc.SkillsDirs) > 0 {
		cfg.SkillsDirs = append([]string(nil), fc.SkillsDirs...)
	}
	if v := strings.TrimSpace(fc.SkillsBootstrap); v != "" {
		cfg.SkillsBootstrap = v
	}
	if fc.SkillsAutoLoad != nil {
		cfg.SkillsAutoLoad = *fc.SkillsAutoLoad
	}
	if fc.SkillsAutoLoadK > 0 {
		cfg.SkillsAutoLoadK = fc.SkillsAutoLoadK
	}
	if fc.SkillsCandidateK > 0 {
		cfg.SkillsCandidateK = fc.SkillsCandidateK
	}
	if fc.SkillsMaxCtx > 0 {
		cfg.SkillsMaxCtx = fc.SkillsMaxCtx
	}
	if fc.CompactionTurns > 0 {
		cfg.CompactionTurns = fc.CompactionTurns
	}
	if fc.CompactionTokens >= 0 {
		cfg.CompactionTokens = fc.CompactionTokens
	}
	if fc.ContextTurnWindow > 0 {
		cfg.ContextTurnWindow = fc.ContextTurnWindow
	}
	if fc.PhaseMaxFiles > 0 {
		cfg.PhaseMaxFiles = fc.PhaseMaxFiles
	}
	if fc.ToolOutputModel > 0 {
		cfg.ToolOutputModel = fc.ToolOutputModel
	}
	if fc.ToolOutputDisplay > 0 {
		cfg.ToolOutputDisplay = fc.ToolOutputDisplay
	}
	if fc.ToolOutputLimit > 0 {
		cfg.ToolOutputLimit = fc.ToolOutputLimit
		if fc.ToolOutputModel <= 0 {
			cfg.ToolOutputModel = fc.ToolOutputLimit
		}
		if fc.ToolOutputDisplay <= 0 {
			cfg.ToolOutputDisplay = fc.ToolOutputLimit
		}
	}
	if v := strings.TrimSpace(fc.RequestTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid request_timeout in yaml: %w", err)
		}
		cfg.RequestTimeout = d
	}
	if v := strings.TrimSpace(fc.TurnTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid turn_timeout in yaml: %w", err)
		}
		cfg.TurnTimeout = d
	}
	if v := strings.TrimSpace(fc.OneShotTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("invalid oneshot_timeout in yaml: %w", err)
		}
		cfg.OneShotTimeout = d
	}
	if v := strings.TrimSpace(fc.NIMBaseURL); v != "" {
		cfg.NIMBaseURL = v
	}
	if fc.LLMMaxRetries >= 0 {
		cfg.LLMMaxRetries = fc.LLMMaxRetries
	}
	if fc.LLMStream != nil {
		cfg.LLMStream = *fc.LLMStream
	}
	if fc.EnableThinking != nil {
		cfg.EnableThinking = *fc.EnableThinking
	}
	if fc.ClearThinking != nil {
		cfg.ClearThinking = *fc.ClearThinking
	}
	if v := strings.TrimSpace(fc.HTTPAddr); v != "" {
		cfg.HTTPAddr = v
	}
	cfg.EnableHTTP = fc.EnableHTTP
	return nil
}

func applyEnvOverrides(cfg *Config) {
	defaults := defaultConfig()
	if v := strings.TrimSpace(os.Getenv("NVIDIA_API_KEY")); v != "" {
		cfg.NVIDIAAPIKey = v
	}
	if v := strings.TrimSpace(os.Getenv("NIM_BASE_URL")); v != "" {
		cfg.NIMBaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv("WORKSPACE_ROOT")); v != "" {
		cfg.WorkspaceRoot = v
	}
	if v := strings.TrimSpace(os.Getenv("RULES_FILE")); v != "" {
		cfg.RulesFile = v
	}
	if v := strings.TrimSpace(os.Getenv("STORAGE_PATH")); v != "" {
		cfg.StoragePath = v
	}
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("EMBEDDING_ENABLED"))); v != "" {
		cfg.EmbeddingEnabled = v == "1" || v == "true" || v == "yes" || v == "on"
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_BASE_URL")); v != "" {
		cfg.EmbeddingBaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_MODEL")); v != "" {
		cfg.EmbeddingModel = v
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_INDEX_PATH")); v != "" {
		cfg.EmbeddingIndex = v
	}
	if v := strings.TrimSpace(os.Getenv("DEFAULT_AGENT")); v != "" {
		cfg.DefaultAgent = v
	}
	if v := strings.TrimSpace(os.Getenv("REQUEST_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.RequestTimeout = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("TURN_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.TurnTimeout = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("ONESHOT_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.OneShotTimeout = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("COMPACTION_TURNS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CompactionTurns = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("CONTEXT_TURN_WINDOW")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ContextTurnWindow = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("PHASE_MAX_FILES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.PhaseMaxFiles = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("COMPACTION_TOKEN_THRESHOLD")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.CompactionTokens = n
		}
	}
	modelExplicit := false
	displayExplicit := false
	if v := strings.TrimSpace(os.Getenv("TOOL_OUTPUT_MODEL_LIMIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ToolOutputModel = n
			cfg.ToolOutputLimit = n
			modelExplicit = true
		}
	}
	if v := strings.TrimSpace(os.Getenv("TOOL_OUTPUT_DISPLAY_LIMIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ToolOutputDisplay = n
			displayExplicit = true
		}
	}
	if v := strings.TrimSpace(os.Getenv("TOOL_OUTPUT_LIMIT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ToolOutputLimit = n
			// Preserve explicit split settings (YAML or env). Only apply legacy
			// mirroring when split values are still defaults and not explicitly set.
			if !modelExplicit && cfg.ToolOutputModel == defaults.ToolOutputModel {
				cfg.ToolOutputModel = n
			}
			if !displayExplicit && cfg.ToolOutputDisplay == defaults.ToolOutputDisplay {
				cfg.ToolOutputDisplay = n
			}
		}
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_TOP_K")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.EmbeddingTopK = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_PER_FILE_MAX")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.EmbeddingPerFile = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_CHUNK_LINES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.EmbeddingChunk = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_CHUNK_OVERLAP")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.EmbeddingOverlap = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_BATCH_SIZE")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.EmbeddingBatch = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_MAX_CONTEXT_CHARS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.EmbeddingMaxCtx = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_IGNORE_DIRS")); v != "" {
		parts := strings.Split(v, ",")
		items := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			items = append(items, p)
		}
		if len(items) > 0 {
			cfg.EmbeddingIgnore = items
		}
	}
	if v := strings.TrimSpace(os.Getenv("EMBEDDING_INIT_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			cfg.EmbeddingInitTime = d
		}
	}
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("RERANK_ENABLED"))); v != "" {
		cfg.RerankEnabled = v == "1" || v == "true" || v == "yes" || v == "on"
	}
	if v := strings.TrimSpace(os.Getenv("RERANK_BASE_URL")); v != "" {
		cfg.RerankBaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv("RERANK_MODEL")); v != "" {
		cfg.RerankModel = v
	}
	if v := strings.TrimSpace(os.Getenv("RERANK_CANDIDATE_K")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.RerankCandidateK = n
		}
	}
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("SKILLS_ENABLED"))); v != "" {
		cfg.SkillsEnabled = v == "1" || v == "true" || v == "yes" || v == "on"
	}
	if v := strings.TrimSpace(os.Getenv("SKILLS_DIRS")); v != "" {
		parts := strings.Split(v, ",")
		items := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			items = append(items, p)
		}
		if len(items) > 0 {
			cfg.SkillsDirs = items
		}
	}
	if v := strings.TrimSpace(os.Getenv("SKILLS_BOOTSTRAP")); v != "" {
		cfg.SkillsBootstrap = v
	}
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("SKILLS_AUTO_LOAD"))); v != "" {
		cfg.SkillsAutoLoad = v == "1" || v == "true" || v == "yes" || v == "on"
	}
	if v := strings.TrimSpace(os.Getenv("SKILLS_AUTO_LOAD_K")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.SkillsAutoLoadK = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SKILLS_CANDIDATE_K")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.SkillsCandidateK = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("SKILLS_MAX_CONTEXT_CHARS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.SkillsMaxCtx = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("LLM_MAX_RETRIES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.LLMMaxRetries = n
		}
	}
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("LLM_STREAM"))); v != "" {
		cfg.LLMStream = v == "1" || v == "true" || v == "yes" || v == "on"
	}
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("ENABLE_THINKING"))); v != "" {
		cfg.EnableThinking = v == "1" || v == "true" || v == "yes" || v == "on"
	}
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("CLEAR_THINKING"))); v != "" {
		cfg.ClearThinking = v == "1" || v == "true" || v == "yes" || v == "on"
	}
	if v := strings.TrimSpace(os.Getenv("AGENT_HTTP_ADDR")); v != "" {
		cfg.HTTPAddr = v
	}
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("AGENT_HTTP_ENABLE"))); v != "" {
		cfg.EnableHTTP = v == "1" || v == "true" || v == "yes" || v == "on"
	}
}

func normalizeAndValidate(cfg *Config) error {
	if strings.TrimSpace(cfg.WorkspaceRoot) == "" {
		return errors.New("workspace_root is required")
	}
	absRoot, err := filepath.Abs(cfg.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("resolve workspace_root: %w", err)
	}
	cfg.WorkspaceRoot = absRoot

	if strings.TrimSpace(cfg.RulesFile) == "" {
		cfg.RulesFile = filepath.Join(cfg.WorkspaceRoot, "AGENTS.md")
	}
	if !filepath.IsAbs(cfg.RulesFile) {
		cfg.RulesFile = filepath.Join(cfg.WorkspaceRoot, cfg.RulesFile)
	}

	if strings.TrimSpace(cfg.StoragePath) == "" {
		cfg.StoragePath = filepath.Join(cfg.WorkspaceRoot, "data", "agent.db")
	}
	if !filepath.IsAbs(cfg.StoragePath) {
		cfg.StoragePath = filepath.Join(cfg.WorkspaceRoot, cfg.StoragePath)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.StoragePath), 0o755); err != nil {
		return fmt.Errorf("ensure storage dir: %w", err)
	}

	if strings.TrimSpace(cfg.EmbeddingIndex) == "" {
		cfg.EmbeddingIndex = filepath.Join(cfg.WorkspaceRoot, "data", "embed-index.jsonl")
	}
	if !filepath.IsAbs(cfg.EmbeddingIndex) {
		cfg.EmbeddingIndex = filepath.Join(cfg.WorkspaceRoot, cfg.EmbeddingIndex)
	}
	if cfg.EmbeddingTopK <= 0 {
		cfg.EmbeddingTopK = 12
	}
	if cfg.EmbeddingPerFile <= 0 {
		cfg.EmbeddingPerFile = 3
	}
	if cfg.EmbeddingChunk <= 0 {
		cfg.EmbeddingChunk = 260
	}
	if cfg.EmbeddingOverlap < 0 {
		cfg.EmbeddingOverlap = 0
	}
	if cfg.EmbeddingOverlap >= cfg.EmbeddingChunk {
		cfg.EmbeddingOverlap = cfg.EmbeddingChunk / 4
	}
	if cfg.EmbeddingBatch <= 0 {
		cfg.EmbeddingBatch = 12
	}
	if cfg.EmbeddingMaxCtx <= 0 {
		cfg.EmbeddingMaxCtx = 12000
	}
	cfg.EmbeddingIgnore = normalizeStringList(cfg.EmbeddingIgnore)
	if len(cfg.EmbeddingIgnore) == 0 {
		cfg.EmbeddingIgnore = []string{".git", ".run", "node_modules", "dist", "build", "vendor", "bin"}
	}
	if cfg.EmbeddingInitTime < 0 {
		cfg.EmbeddingInitTime = 0
	}
	if cfg.RerankCandidateK <= 0 {
		cfg.RerankCandidateK = 20
	}
	if cfg.RerankEnabled && !cfg.EmbeddingEnabled {
		cfg.RerankEnabled = false
	}
	if cfg.EmbeddingEnabled {
		if strings.TrimSpace(cfg.EmbeddingBaseURL) == "" {
			return errors.New("embedding_base_url is required when embedding_enabled=true")
		}
		if strings.TrimSpace(cfg.EmbeddingModel) == "" {
			return errors.New("embedding_model is required when embedding_enabled=true")
		}
	}
	if cfg.RerankEnabled {
		if strings.TrimSpace(cfg.RerankBaseURL) == "" {
			return errors.New("rerank_base_url is required when rerank_enabled=true")
		}
		if strings.TrimSpace(cfg.RerankModel) == "" {
			return errors.New("rerank_model is required when rerank_enabled=true")
		}
	}
	cfg.SkillsDirs = normalizeStringList(cfg.SkillsDirs)
	for i, dir := range cfg.SkillsDirs {
		if !filepath.IsAbs(dir) {
			cfg.SkillsDirs[i] = filepath.Join(cfg.WorkspaceRoot, dir)
		}
	}
	if cfg.SkillsAutoLoadK <= 0 {
		cfg.SkillsAutoLoadK = 2
	}
	if cfg.SkillsCandidateK <= 0 {
		cfg.SkillsCandidateK = 12
	}
	if cfg.SkillsCandidateK < cfg.SkillsAutoLoadK {
		cfg.SkillsCandidateK = cfg.SkillsAutoLoadK
	}
	if cfg.SkillsMaxCtx <= 0 {
		cfg.SkillsMaxCtx = 4000
	}
	if cfg.PhaseMaxFiles <= 0 {
		cfg.PhaseMaxFiles = 5
	}
	if cfg.PhaseMaxFiles > 20 {
		cfg.PhaseMaxFiles = 20
	}
	if cfg.ToolOutputModel <= 0 {
		if cfg.ToolOutputLimit > 0 {
			cfg.ToolOutputModel = cfg.ToolOutputLimit
		} else {
			cfg.ToolOutputModel = 12 * 1024
		}
	}
	if cfg.ToolOutputDisplay <= 0 {
		if cfg.ToolOutputLimit > 0 {
			cfg.ToolOutputDisplay = cfg.ToolOutputLimit
		} else {
			cfg.ToolOutputDisplay = 50 * 1024
		}
	}
	if cfg.ToolOutputDisplay < cfg.ToolOutputModel {
		cfg.ToolOutputDisplay = cfg.ToolOutputModel
	}
	cfg.ToolOutputLimit = cfg.ToolOutputModel

	if strings.TrimSpace(cfg.NVIDIAAPIKey) == "" {
		return errors.New("NVIDIA_API_KEY is required")
	}
	if strings.TrimSpace(cfg.NIMBaseURL) == "" {
		return errors.New("nim_base_url is required")
	}
	if cfg.CompactionTokens < 0 {
		cfg.CompactionTokens = 0
	}
	if cfg.LLMMaxRetries < 0 {
		cfg.LLMMaxRetries = 0
	}
	if cfg.LLMMaxRetries > 6 {
		cfg.LLMMaxRetries = 6
	}
	if cfg.RequestTimeout < 0 {
		cfg.RequestTimeout = 0
	}
	if cfg.TurnTimeout < 0 {
		cfg.TurnTimeout = 0
	}
	if cfg.OneShotTimeout < 0 {
		cfg.OneShotTimeout = 0
	}
	if len(cfg.Agents) == 0 {
		return errors.New("at least one agent must be configured")
	}
	if strings.TrimSpace(cfg.DefaultAgent) == "" {
		cfg.DefaultAgent = "build"
	}
	if _, ok := cfg.Agents[cfg.DefaultAgent]; !ok {
		return fmt.Errorf("default_agent %q not found in agents config", cfg.DefaultAgent)
	}
	for name, ac := range cfg.Agents {
		if ac.MaxTokens <= 0 {
			ac.MaxTokens = 1600
		}
		if ac.MaxTokens > 131071 {
			ac.MaxTokens = 131071
		}
		if ac.Temperature < 0 {
			ac.Temperature = 0
		}
		if ac.Temperature > 1 {
			ac.Temperature = 1
		}
		if strings.TrimSpace(ac.Model) == "" {
			ac.Model = "z-ai/glm5"
		}
		if len(ac.Tools) == 0 {
			ac.Tools = []string{"list", "glob", "grep", "read", "mkdir", "write_file", "edit", "patch", "apply_diff", "verify", "bash", "webfetch", "websearch", "skill"}
		}
		if ac.Permissions == nil {
			ac.Permissions = map[string]string{"*": "ask"}
		}
		cfg.Agents[name] = ac
	}
	return nil
}

func loadDotEnv(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:idx])
		v := strings.TrimSpace(line[idx+1:])
		if (strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"")) || (strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'")) {
			v = strings.Trim(v, "\"'")
		}
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
	return nil
}

func normalizeStringList(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
