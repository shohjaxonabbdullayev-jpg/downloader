package platforms

import (
	"os"
	"path/filepath"
	"sort"
)

func allFiles(dir string) []string {
	var files []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func totalSize(files []string) int64 {
	var sum int64
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && !st.IsDir() {
			sum += st.Size()
		}
	}
	return sum
}

