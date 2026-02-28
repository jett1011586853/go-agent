package indexer

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Updater interface {
	ReindexPaths(ctx context.Context, paths []string) error
}

type Options struct {
	Debounce time.Duration
	Buffer   int
	Timeout  time.Duration
}

type Service struct {
	rootAbs string
	updater Updater
	opts    Options

	input chan string

	mu      sync.Mutex
	pending map[string]struct{}
}

func NewService(root string, updater Updater, opts Options) *Service {
	if opts.Debounce <= 0 {
		opts.Debounce = 700 * time.Millisecond
	}
	if opts.Buffer <= 0 {
		opts.Buffer = 1024
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	rootAbs, _ := filepath.Abs(strings.TrimSpace(root))
	return &Service{
		rootAbs: rootAbs,
		updater: updater,
		opts:    opts,
		input:   make(chan string, opts.Buffer),
		pending: map[string]struct{}{},
	}
}

func (s *Service) Start(ctx context.Context, onError func(error)) {
	if s == nil || s.updater == nil {
		return
	}
	go s.loop(ctx, onError)
}

func (s *Service) Enqueue(paths ...string) {
	if s == nil || len(paths) == 0 {
		return
	}
	for _, p := range paths {
		rel := s.normalizeRelPath(p)
		if rel == "" {
			continue
		}
		select {
		case s.input <- rel:
		default:
			// If queue is full, drop this single event; watcher + hooks will
			// continue producing later events, and batch updates still converge.
		}
	}
}

func (s *Service) loop(ctx context.Context, onError func(error)) {
	timer := time.NewTimer(s.opts.Debounce)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}

	for {
		select {
		case <-ctx.Done():
			s.flush(ctx, onError)
			return
		case p := <-s.input:
			s.mu.Lock()
			s.pending[p] = struct{}{}
			s.mu.Unlock()
			timer.Reset(s.opts.Debounce)
		case <-timer.C:
			s.flush(ctx, onError)
		}
	}
}

func (s *Service) flush(parent context.Context, onError func(error)) {
	paths := s.takePending()
	if len(paths) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(parent, s.opts.Timeout)
	defer cancel()
	if err := s.updater.ReindexPaths(ctx, paths); err != nil && onError != nil {
		onError(err)
	}
}

func (s *Service) takePending() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.pending))
	for p := range s.pending {
		out = append(out, p)
	}
	s.pending = map[string]struct{}{}
	return out
}

func (s *Service) normalizeRelPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if p == "." {
		return ""
	}
	if filepath.IsAbs(p) {
		if s.rootAbs == "" {
			return filepath.ToSlash(p)
		}
		abs := filepath.Clean(p)
		rel, err := filepath.Rel(s.rootAbs, abs)
		if err != nil {
			return ""
		}
		if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return ""
		}
		p = rel
	}
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return ""
	}
	return p
}
