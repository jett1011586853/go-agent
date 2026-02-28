package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go-agent/internal/agent"
	"go-agent/internal/app"
	"go-agent/internal/config"
	ctxbuild "go-agent/internal/context"
	"go-agent/internal/embednim"
	"go-agent/internal/indexer"
	"go-agent/internal/llm"
	"go-agent/internal/permission"
	"go-agent/internal/reranknim"
	"go-agent/internal/retrieval"
	"go-agent/internal/server"
	"go-agent/internal/session"
	"go-agent/internal/storage"
	"go-agent/internal/tool"
)

func main() {
	var (
		configPath string
		sessionID  string
		agentName  string
		task       string
		httpMode   bool
		httpAddr   string
	)
	flag.StringVar(&configPath, "config", "opencode/agent.yaml", "yaml config path")
	flag.StringVar(&sessionID, "session", "", "existing session id")
	flag.StringVar(&agentName, "agent", "", "default agent (build|plan)")
	flag.StringVar(&task, "task", "", "one-shot task")
	flag.BoolVar(&httpMode, "http", false, "run HTTP server mode")
	flag.StringVar(&httpAddr, "http-addr", "", "http listen address")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatal(err)
	}
	if strings.TrimSpace(httpAddr) != "" {
		cfg.HTTPAddr = strings.TrimSpace(httpAddr)
	}
	if httpMode {
		cfg.EnableHTTP = true
	}

	store, err := storage.NewBoltStore(cfg.StoragePath)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	sessionMgr := session.NewManager(store, cfg.CompactionTurns, cfg.CompactionTokens)
	agents, err := agent.NewManager(cfg)
	if err != nil {
		log.Fatal(err)
	}
	registry := tool.NewRegistry()
	if err := tool.RegisterBuiltins(registry, cfg.WorkspaceRoot, cfg.ToolOutputLimit); err != nil {
		log.Fatal(err)
	}
	permEngine := permission.NewEngine(permission.DecisionAsk)
	llmProvider := llm.NewNVIDIAProvider(
		cfg.NIMBaseURL,
		cfg.NVIDIAAPIKey,
		cfg.RequestTimeout,
		cfg.LLMMaxRetries,
		cfg.LLMStream,
		cfg.EnableThinking,
		cfg.ClearThinking,
	)
	contextBuilder := ctxbuild.NewBuilder(cfg.RulesFile, cfg.WorkspaceRoot, cfg.ContextTurnWindow)

	application := app.New(cfg, agents, sessionMgr, contextBuilder, permEngine, registry, llmProvider)
	indexCtx, cancelIndexer := context.WithCancel(context.Background())
	defer cancelIndexer()
	if cfg.EmbeddingEnabled {
		log.Printf(
			"embedding retrieval enabled: base=%s model=%s index=%s topk=%d per_file=%d chunk_lines=%d overlap=%d",
			cfg.EmbeddingBaseURL,
			cfg.EmbeddingModel,
			cfg.EmbeddingIndex,
			cfg.EmbeddingTopK,
			cfg.EmbeddingPerFile,
			cfg.EmbeddingChunk,
			cfg.EmbeddingOverlap,
		)
		embedClient := embednim.New(cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.RequestTimeout)
		retriever := retrieval.NewService(embedClient, retrieval.Options{
			Root:             cfg.WorkspaceRoot,
			IndexPath:        cfg.EmbeddingIndex,
			ChunkLines:       cfg.EmbeddingChunk,
			ChunkOverlap:     cfg.EmbeddingOverlap,
			BatchSize:        cfg.EmbeddingBatch,
			TopK:             cfg.EmbeddingTopK,
			PerFileLimit:     cfg.EmbeddingPerFile,
			MaxContextChars:  cfg.EmbeddingMaxCtx,
			IgnoreDirs:       cfg.EmbeddingIgnore,
			RerankCandidateK: cfg.RerankCandidateK,
		})
		if cfg.RerankEnabled {
			rerankTimeout := cfg.RequestTimeout
			if rerankTimeout <= 0 || rerankTimeout > 20*time.Second {
				rerankTimeout = 20 * time.Second
			}
			retriever.SetReranker(reranknim.New(cfg.RerankBaseURL, cfg.RerankModel, rerankTimeout))
			log.Printf(
				"retrieval rerank enabled: base=%s model=%s candidate_k=%d",
				cfg.RerankBaseURL,
				cfg.RerankModel,
				cfg.RerankCandidateK,
			)
		} else {
			log.Printf("retrieval rerank disabled: rerank_enabled=false")
		}
		initCtx, cancel := withOptionalTimeout(context.Background(), cfg.EmbeddingInitTime)
		err := retriever.LoadOrBuild(initCtx, false)
		cancel()
		if err != nil {
			log.Printf("warning: embedding retrieval disabled (init failed): %v", err)
		} else {
			application.SetRetriever(retriever)
			log.Printf(
				"embedding retrieval ready: chunks=%d index=%s base=%s model=%s",
				retriever.ChunkCount(),
				cfg.EmbeddingIndex,
				cfg.EmbeddingBaseURL,
				cfg.EmbeddingModel,
			)
			idxService := indexer.NewService(cfg.WorkspaceRoot, retriever, indexer.Options{})
			idxService.Start(indexCtx, func(err error) {
				log.Printf("warning: indexer update failed: %v", err)
			})
			if err := registry.RegisterHook(indexer.NewToolHook(idxService)); err != nil {
				log.Printf("warning: failed to register index hook: %v", err)
			}
			go func() {
				if err := indexer.StartWorkspaceWatcher(
					indexCtx,
					cfg.WorkspaceRoot,
					cfg.EmbeddingIgnore,
					idxService.Enqueue,
					func(err error) {
						log.Printf("warning: index watcher error: %v", err)
					},
				); err != nil {
					log.Printf("warning: index watcher disabled: %v", err)
				} else {
					log.Printf("embedding index watcher started on %s", cfg.WorkspaceRoot)
				}
			}()
		}
	} else {
		log.Printf("embedding retrieval disabled: embedding_enabled=false")
	}

	if cfg.EnableHTTP {
		srv := server.New(application)
		httpSrv := &http.Server{
			Addr:              cfg.HTTPAddr,
			Handler:           srv.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
		}
		log.Printf("open-code style agent HTTP server listening on %s", cfg.HTTPAddr)
		log.Fatal(httpSrv.ListenAndServe())
		return
	}

	if strings.TrimSpace(task) != "" {
		runOneShot(application, cfg, sessionID, agentName, task)
		return
	}
	runREPL(application, cfg, sessionID, agentName)
}

func runOneShot(a *app.App, cfg config.Config, sessionID, agentName, task string) {
	ctx, cancel := withOptionalTimeout(context.Background(), cfg.OneShotTimeout)
	defer cancel()
	events := newCLIEventPrinter()
	ctx = app.WithEventHandler(ctx, events.Handle)
	stream := newCLIStreamPrinter()
	ctx = llm.WithStreamHandler(ctx, stream.Handle)
	resp, err := a.HandleTurn(ctx, app.TurnRequest{
		SessionID: sessionID,
		Agent:     agentName,
		Input:     task,
	}, cliApprover{})
	rest := stream.FinishAndRemainder(resp.Output)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("session=%s agent=%s\n\n", resp.SessionID, resp.Agent)
	if strings.TrimSpace(rest) != "" {
		fmt.Printf("%s\n", rest)
	}
}

func runREPL(a *app.App, cfg config.Config, initSessionID, initAgent string) {
	fmt.Println("go-agent CLI (OpenCode-style)")
	fmt.Println("Commands: /help  /exit  /new  /sessions  /use <sessionId>  /agent <name>")

	currentSession := strings.TrimSpace(initSessionID)
	currentAgent := strings.TrimSpace(initAgent)
	if currentSession == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s, err := a.CreateSession(ctx)
		cancel()
		if err != nil {
			log.Fatalf("create session: %v", err)
		}
		currentSession = s.ID
	}
	fmt.Printf("Current session: %s\n", currentSession)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for {
		fmt.Printf("\n[%s|%s] > ", currentSession, defaultText(currentAgent, "build"))
		if !scanner.Scan() {
			fmt.Println()
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			args := strings.Fields(line)
			switch strings.ToLower(args[0]) {
			case "/help":
				printHelp()
			case "/exit", "/quit":
				return
			case "/new":
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				s, err := a.CreateSession(ctx)
				cancel()
				if err != nil {
					fmt.Printf("error: %v\n", err)
					continue
				}
				currentSession = s.ID
				fmt.Printf("new session: %s\n", currentSession)
			case "/sessions":
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				items, err := a.ListSessionIDs(ctx, 30)
				cancel()
				if err != nil {
					fmt.Printf("error: %v\n", err)
					continue
				}
				for _, id := range items {
					fmt.Println("-", id)
				}
			case "/use":
				if len(args) < 2 {
					fmt.Println("usage: /use <sessionId>")
					continue
				}
				currentSession = strings.TrimSpace(args[1])
			case "/agent":
				if len(args) < 2 {
					fmt.Println("usage: /agent <build|plan|...>")
					continue
				}
				currentAgent = strings.TrimSpace(args[1])
			default:
				fmt.Println("unknown command, run /help")
			}
			continue
		}

		ctx, cancel := withOptionalTimeout(context.Background(), cfg.TurnTimeout)
		events := newCLIEventPrinter()
		ctx = app.WithEventHandler(ctx, events.Handle)
		stream := newCLIStreamPrinter()
		ctx = llm.WithStreamHandler(ctx, stream.Handle)
		start := time.Now()
		resp, err := a.HandleTurn(ctx, app.TurnRequest{
			SessionID: currentSession,
			Agent:     currentAgent,
			Input:     line,
		}, cliApprover{})
		cancel()
		rest := stream.FinishAndRemainder(resp.Output)
		if err != nil {
			fmt.Printf("error: %v\n", err)
			continue
		}
		currentSession = resp.SessionID
		if strings.TrimSpace(rest) != "" {
			fmt.Printf("\n%s\n", rest)
		}
		fmt.Printf("(done in %s)\n", time.Since(start).Round(time.Millisecond))
	}
}

type cliStreamPrinter struct {
	mu       sync.Mutex
	started  bool
	rendered strings.Builder
}

func newCLIStreamPrinter() *cliStreamPrinter {
	return &cliStreamPrinter{}
}

func (p *cliStreamPrinter) Handle(delta llm.StreamDelta) {
	text := delta.Content
	if strings.TrimSpace(text) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		fmt.Print("\n[writer]\n")
		p.started = true
	}
	fmt.Print(text)
	p.rendered.WriteString(text)
}

func (p *cliStreamPrinter) FinishAndRemainder(full string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		fmt.Println()
		p.started = false
	}
	rendered := p.rendered.String()
	p.rendered.Reset()
	if rendered == "" {
		return full
	}
	if strings.HasPrefix(full, rendered) {
		return strings.TrimPrefix(full, rendered)
	}
	if full == rendered {
		return ""
	}
	// Fallback: return full when we cannot confidently align prefix.
	return full
}

type cliEventPrinter struct {
	mu sync.Mutex
}

func newCLIEventPrinter() *cliEventPrinter {
	return &cliEventPrinter{}
}

func (p *cliEventPrinter) Handle(ev app.Event) {
	if p == nil {
		return
	}
	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch strings.ToLower(strings.TrimSpace(ev.Type)) {
	case "planner":
		fmt.Printf("\n[planner] %s\n", text)
	case "writer":
		fmt.Printf("\n[writer] %s\n", text)
	case "retrieval":
		fmt.Printf("\n[retrieval]\n%s\n", text)
	case "tool":
		fmt.Printf("\n[tool]\n%s\n", text)
	default:
		fmt.Printf("\n[meta] %s\n", text)
	}
}

func printHelp() {
	fmt.Println("Commands:")
	fmt.Println("  /help                 Show help")
	fmt.Println("  /exit                 Exit CLI")
	fmt.Println("  /new                  Create and switch to new session")
	fmt.Println("  /sessions             List recent sessions")
	fmt.Println("  /use <sessionId>      Switch session")
	fmt.Println("  /agent <name>         Set default agent for next turns")
	fmt.Println("")
	fmt.Println("Tips:")
	fmt.Println("  1) Prefix your input with @plan or @build to override agent per turn.")
	fmt.Println("  2) Approval prompts appear for tools with permission=ask.")
}

type cliApprover struct{}

func (cliApprover) Approve(_ context.Context, p app.ApprovalPrompt) (bool, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("\n[approval required]")
	fmt.Printf("tool: %s\n", p.ToolName)
	fmt.Printf("rule: %s\n", p.Rule)
	fmt.Printf("args: %s\n", clip(string(p.Args), 600))
	fmt.Print("allow? [y/N]: ")
	text, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func clip(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

func defaultText(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func withOptionalTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, d)
}
