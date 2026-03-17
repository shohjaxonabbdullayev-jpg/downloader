package worker

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StartTTLReaper periodically removes directories under root that start with prefix and are older than ttl.
// It is best-effort: errors are ignored.
func StartTTLReaper(root, prefix string, ttl time.Duration, interval time.Duration) func() {
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	if interval <= 0 {
		interval = 30 * time.Minute
	}

	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()

		reap := func() {
			entries, err := os.ReadDir(root)
			if err != nil {
				return
			}
			cutoff := time.Now().Add(-ttl)
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				if !strings.HasPrefix(name, prefix) {
					continue
				}
				path := filepath.Join(root, name)
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				if info.ModTime().Before(cutoff) {
					_ = os.RemoveAll(path)
				}
			}
		}

		// run once quickly
		reap()

		for {
			select {
			case <-stop:
				return
			case <-t.C:
				reap()
			}
		}
	}()

	return func() { close(stop) }
}

