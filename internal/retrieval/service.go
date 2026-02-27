package retrieval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
)

type Embedder interface {
	Embed(ctx context.Context, texts []string, inputType string) ([][]float64, error)
}

type Chunk struct {
	Path      string    `json:"path"`
	StartLine int       `json:"start_line"`
	EndLine   int       `json:"end_line"`
	Text      string    `json:"text"`
	Vec       []float64 `json:"vec"`
}

type ScoredChunk struct {
	Chunk Chunk
	Score float64
}

type Options struct {
	Root            string
	IndexPath       string
	ChunkLines      int
	ChunkOverlap    int
	BatchSize       int
	TopK            int
	PerFileLimit    int
	MaxChunkChars   int
	MaxContextChars int
	MaxFileBytes    int64
	IgnoreDirs      []string
}

func DefaultOptions(root string) Options {
	return Options{
		Root:            strings.TrimSpace(root),
		IndexPath:       filepath.Join(strings.TrimSpace(root), "data", "embed-index.jsonl"),
		ChunkLines:      260,
		ChunkOverlap:    50,
		BatchSize:       12,
		TopK:            12,
		PerFileLimit:    3,
		MaxChunkChars:   2400,
		MaxContextChars: 28000,
		MaxFileBytes:    2 * 1024 * 1024,
		IgnoreDirs: []string{
			".git",
			".run",
			"node_modules",
			"dist",
			"build",
			"vendor",
			"bin",
		},
	}
}

type Service struct {
	embedder Embedder
	opts     Options

	mu     sync.RWMutex
	chunks []Chunk
}

func NewService(embedder Embedder, opts Options) *Service {
	o := normalizeOptions(opts)
	return &Service{
		embedder: embedder,
		opts:     o,
	}
}

func (s *Service) LoadOrBuild(ctx context.Context, force bool) error {
	if s.embedder == nil {
		return fmt.Errorf("retrieval embedder is nil")
	}
	if strings.TrimSpace(s.opts.Root) == "" {
		return fmt.Errorf("retrieval root is empty")
	}
	if strings.TrimSpace(s.opts.IndexPath) == "" {
		return fmt.Errorf("retrieval index path is empty")
	}

	if !force {
		chunks, err := loadJSONL(s.opts.IndexPath)
		switch {
		case err == nil && len(chunks) > 0:
			s.mu.Lock()
			s.chunks = chunks
			s.mu.Unlock()
			return nil
		case err == nil && len(chunks) == 0:
			// Continue to build if cache exists but is empty.
		case errors.Is(err, os.ErrNotExist):
			// Continue to build.
		default:
			// Corrupted cache should not block startup; rebuild.
		}
	}

	chunks, err := buildChunks(s.opts.Root, s.opts)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return fmt.Errorf("no indexable chunks found under %s", s.opts.Root)
	}
	if err := embedAllChunks(ctx, s.embedder, chunks, s.opts.BatchSize); err != nil {
		return err
	}
	if err := saveJSONL(s.opts.IndexPath, chunks); err != nil {
		return err
	}

	s.mu.Lock()
	s.chunks = chunks
	s.mu.Unlock()
	return nil
}

func (s *Service) Retrieve(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", nil
	}

	s.mu.RLock()
	chunks := append([]Chunk(nil), s.chunks...)
	opts := s.opts
	s.mu.RUnlock()
	if len(chunks) == 0 {
		return "", nil
	}

	vecs, err := s.embedder.Embed(ctx, []string{query}, "query")
	if err != nil {
		return "", fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return "", fmt.Errorf("embed query returned empty vector")
	}

	scored := topKWithDiversity(vecs[0], chunks, opts.TopK, opts.PerFileLimit)
	return formatContext(scored, opts.MaxContextChars), nil
}

func (s *Service) ChunkCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.chunks)
}

func normalizeOptions(opts Options) Options {
	def := DefaultOptions(opts.Root)
	if strings.TrimSpace(opts.Root) == "" {
		opts.Root = def.Root
	}
	if strings.TrimSpace(opts.IndexPath) == "" {
		opts.IndexPath = def.IndexPath
	}
	if opts.ChunkLines <= 0 {
		opts.ChunkLines = def.ChunkLines
	}
	if opts.ChunkOverlap < 0 {
		opts.ChunkOverlap = 0
	}
	if opts.ChunkOverlap >= opts.ChunkLines {
		opts.ChunkOverlap = opts.ChunkLines / 4
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = def.BatchSize
	}
	if opts.TopK <= 0 {
		opts.TopK = def.TopK
	}
	if opts.PerFileLimit <= 0 {
		opts.PerFileLimit = def.PerFileLimit
	}
	if opts.MaxChunkChars <= 0 {
		opts.MaxChunkChars = def.MaxChunkChars
	}
	if opts.MaxContextChars <= 0 {
		opts.MaxContextChars = def.MaxContextChars
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = def.MaxFileBytes
	}
	if len(opts.IgnoreDirs) == 0 {
		opts.IgnoreDirs = def.IgnoreDirs
	}
	opts.Root = strings.TrimSpace(opts.Root)
	opts.IndexPath = strings.TrimSpace(opts.IndexPath)
	return opts
}

func buildChunks(root string, opts Options) ([]Chunk, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("workspace root is empty")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}

	ignore := make(map[string]struct{}, len(opts.IgnoreDirs))
	for _, name := range opts.IgnoreDirs {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		ignore[name] = struct{}{}
	}

	out := make([]Chunk, 0, 1024)
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if path == absRoot {
				return nil
			}
			name := strings.ToLower(strings.TrimSpace(d.Name()))
			if _, ok := ignore[name]; ok {
				return fs.SkipDir
			}
			if strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !isLikelySourceFile(d.Name()) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if opts.MaxFileBytes > 0 && info.Size() > opts.MaxFileBytes {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil || len(raw) == 0 {
			return nil
		}
		if !isLikelyText(raw) {
			return nil
		}

		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		pieces := chunkText(rel, text, opts.ChunkLines, opts.ChunkOverlap, opts.MaxChunkChars)
		out = append(out, pieces...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func chunkText(path, text string, chunkLines, overlap, maxChars int) []Chunk {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return nil
	}
	step := chunkLines - overlap
	if step <= 0 {
		step = chunkLines
	}
	if step <= 0 {
		step = 200
	}

	chunks := make([]Chunk, 0, (len(lines)/step)+1)
	for start := 0; start < len(lines); start += step {
		end := start + chunkLines
		if end > len(lines) {
			end = len(lines)
		}
		block := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
		if block != "" {
			block = clipRunes(block, maxChars)
			chunks = append(chunks, Chunk{
				Path:      path,
				StartLine: start + 1,
				EndLine:   end,
				Text:      block,
			})
		}
		if end == len(lines) {
			break
		}
	}
	return chunks
}

func embedAllChunks(ctx context.Context, embedder Embedder, chunks []Chunk, batchSize int) error {
	if batchSize <= 0 {
		batchSize = 8
	}
	for i := 0; i < len(chunks); i += batchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := i + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		texts := make([]string, 0, end-i)
		for j := i; j < end; j++ {
			texts = append(texts, chunks[j].Text)
		}
		vecs, err := embedder.Embed(ctx, texts, "document")
		if err != nil {
			return fmt.Errorf("embed document chunks [%d:%d]: %w", i, end, err)
		}
		if len(vecs) != len(texts) {
			return fmt.Errorf("embed size mismatch for batch [%d:%d], want=%d got=%d", i, end, len(texts), len(vecs))
		}
		for j := range vecs {
			if len(vecs[j]) == 0 {
				return fmt.Errorf("empty vector for chunk %d", i+j)
			}
			chunks[i+j].Vec = vecs[j]
		}
	}
	return nil
}

func saveJSONL(path string, chunks []Chunk) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("index path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 1024*1024)
	for _, ch := range chunks {
		raw, err := json.Marshal(ch)
		if err != nil {
			return err
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.Flush()
}

func loadJSONL(path string) ([]Chunk, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := make([]Chunk, 0, 1024)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c Chunk
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("parse index %s line %d: %w", path, lineNo, err)
		}
		if strings.TrimSpace(c.Path) == "" || len(c.Vec) == 0 || strings.TrimSpace(c.Text) == "" {
			continue
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func topKWithDiversity(queryVec []float64, chunks []Chunk, k int, perFile int) []ScoredChunk {
	if len(queryVec) == 0 || len(chunks) == 0 || k <= 0 {
		return nil
	}
	all := make([]ScoredChunk, 0, len(chunks))
	for _, ch := range chunks {
		if len(ch.Vec) == 0 {
			continue
		}
		all = append(all, ScoredChunk{
			Chunk: ch,
			Score: cosine(queryVec, ch.Vec),
		})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Score == all[j].Score {
			if all[i].Chunk.Path == all[j].Chunk.Path {
				return all[i].Chunk.StartLine < all[j].Chunk.StartLine
			}
			return all[i].Chunk.Path < all[j].Chunk.Path
		}
		return all[i].Score > all[j].Score
	})

	out := make([]ScoredChunk, 0, k)
	perPathCount := map[string]int{}
	for _, candidate := range all {
		if perFile > 0 && perPathCount[candidate.Chunk.Path] >= perFile {
			continue
		}
		out = append(out, candidate)
		perPathCount[candidate.Chunk.Path]++
		if len(out) >= k {
			break
		}
	}
	return out
}

func formatContext(items []ScoredChunk, maxChars int) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for i, it := range items {
		block := fmt.Sprintf(
			"[CONTEXT %d] %s:%d-%d (score=%.4f)\n%s\n\n",
			i+1,
			it.Chunk.Path,
			it.Chunk.StartLine,
			it.Chunk.EndLine,
			it.Score,
			strings.TrimSpace(it.Chunk.Text),
		)
		blockRunes := utf8.RuneCountInString(block)
		if maxChars > 0 && used+blockRunes > maxChars {
			remain := maxChars - used
			if remain <= 0 {
				break
			}
			b.WriteString(clipRunes(block, remain))
			break
		}
		b.WriteString(block)
		used += blockRunes
	}
	return strings.TrimSpace(b.String())
}

func cosine(a, b []float64) float64 {
	var dot, na, nb float64
	maxLen := len(a)
	if len(b) < maxLen {
		maxLen = len(b)
	}
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

func isLikelySourceFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case
		".go", ".mod", ".sum",
		".py", ".js", ".jsx", ".ts", ".tsx",
		".java", ".kt", ".rs", ".c", ".h", ".cpp", ".hpp",
		".cs", ".swift", ".php", ".rb",
		".sh", ".ps1", ".bat",
		".md", ".txt", ".rst",
		".yaml", ".yml", ".json", ".toml", ".ini", ".conf",
		".sql", ".graphql", ".proto",
		".html", ".css", ".scss", ".less", ".xml":
		return true
	default:
		return false
	}
}

func isLikelyText(raw []byte) bool {
	sample := raw
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if len(sample) == 0 {
		return false
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return false
	}
	if !utf8.Valid(sample) {
		return false
	}
	var nonText int
	for _, b := range sample {
		if b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		if b < 0x20 {
			nonText++
		}
	}
	return float64(nonText)/float64(len(sample)) < 0.05
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
