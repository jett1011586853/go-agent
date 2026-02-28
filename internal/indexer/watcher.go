package indexer

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

func StartWorkspaceWatcher(
	ctx context.Context,
	root string,
	ignoreDirs []string,
	enqueue func(paths ...string),
	onError func(error),
) error {
	root = strings.TrimSpace(root)
	if root == "" || enqueue == nil {
		return nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	ignored := make(map[string]struct{}, len(ignoreDirs))
	for _, dir := range ignoreDirs {
		dir = strings.ToLower(strings.TrimSpace(dir))
		if dir != "" {
			ignored[dir] = struct{}{}
		}
	}

	isIgnoredDir := func(name string) bool {
		_, ok := ignored[strings.ToLower(strings.TrimSpace(name))]
		return ok
	}

	addDirRecursive := func(path string) error {
		return filepath.WalkDir(path, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			if p != path && isIgnoredDir(d.Name()) {
				return fs.SkipDir
			}
			return watcher.Add(p)
		})
	}

	if err := addDirRecursive(absRoot); err != nil {
		_ = watcher.Close()
		return err
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-watcher.Errors:
				if err != nil && onError != nil {
					onError(err)
				}
			case ev := <-watcher.Events:
				// Newly created directories need watches added.
				if ev.Op&fsnotify.Create != 0 {
					if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
						if !isIgnoredDir(filepath.Base(ev.Name)) {
							_ = addDirRecursive(ev.Name)
						}
						continue
					}
				}

				if shouldIgnorePath(absRoot, ev.Name, isIgnoredDir) {
					continue
				}
				if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) != 0 {
					enqueue(ev.Name)
				}
			}
		}
	}()
	return nil
}

func shouldIgnorePath(absRoot, path string, isIgnoredDir func(string) bool) bool {
	rel, err := filepath.Rel(absRoot, path)
	if err != nil {
		return true
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return true
	}
	parts := strings.Split(rel, "/")
	for i := 0; i < len(parts)-1; i++ {
		if isIgnoredDir(parts[i]) {
			return true
		}
	}
	return false
}
