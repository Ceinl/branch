package main

import (
	"os"
	"time"
	"unicode/utf8"
)

const watchInterval = 2 * time.Second

// watchExternalChanges polls the files that currently have viewers and
// broadcasts changes made outside Branch (vim, git pull, scripts) into the
// live collaboration stream. Saves made through Branch also touch mtimes,
// but clients ignore updates whose content they already have.
func (a *app) watchExternalChanges() {
	seen := map[string]time.Time{}
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for range ticker.C {
		active := map[string]bool{}
		for _, path := range a.collab.activePaths() {
			active[path] = true
			full, _, err := a.resolveExisting(path)
			if err != nil {
				continue
			}
			info, err := os.Stat(full)
			if err != nil || info.IsDir() || info.Size() > maxEditableBytes {
				continue
			}
			mod := info.ModTime()
			last, known := seen[path]
			seen[path] = mod
			if !known || mod.Equal(last) {
				continue
			}
			data, err := os.ReadFile(full)
			if err != nil || !utf8.Valid(data) {
				continue
			}
			a.collab.broadcastUpdate(path, string(data), mod.Format(time.RFC3339),
				authUser{ID: "external", Name: "External change"}, "")
		}
		for path := range seen {
			if !active[path] {
				delete(seen, path)
			}
		}
	}
}
