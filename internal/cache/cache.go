package cache

import (
	"io"
	"os"
	"path/filepath"
	"sort"
)

type FileCache struct {
	Root string // e.g. downloads/cache
}

func (c FileCache) CacheDir(key string) string {
	return filepath.Join(c.Root, key)
}

func (c FileCache) Has(key string) (files []string, ok bool) {
	dir := c.CacheDir(key)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files, len(files) > 0
}

func (c FileCache) Save(key string, srcFiles []string) ([]string, error) {
	dir := c.CacheDir(key)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	var out []string
	for _, src := range srcFiles {
		dst := filepath.Join(dir, filepath.Base(src))
		if err := copyFile(dst, src); err != nil {
			return nil, err
		}
		out = append(out, dst)
	}
	sort.Strings(out)
	return out, nil
}

func copyFile(dst, src string) error {
	// Fast path: hardlink (same filesystem). This avoids copying large media.
	// If it fails (e.g. different volumes), fall back to copy.
	_ = os.Remove(dst)
	if err := os.Link(src, dst); err == nil {
		return nil
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

