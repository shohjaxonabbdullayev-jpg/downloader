package downloader

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"telegram_bot_downloader/internal/cache"
	"telegram_bot_downloader/internal/platforms"
	"telegram_bot_downloader/internal/worker"
)

type PipelineDownloader struct {
	Detector   YtDlpDetector
	Registry   platforms.Registry
	Cache      cache.FileCache
	Semaphore  *worker.Semaphore

	DownloadsRoot string // e.g. "downloads"
	JobTTL        time.Duration
	Logger        func(format string, args ...any)
}

func (p *PipelineDownloader) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (p *PipelineDownloader) Detect(ctx context.Context, url string) (*MediaInfo, error) {
	u := NormalizeURL(url)
	return p.Detector.Detect(ctx, u)
}

func (p *PipelineDownloader) Download(ctx context.Context, url string, jobDir string) (*DownloadResult, error) {
	info, _ := p.Detector.Detect(ctx, NormalizeURL(url))
	return p.DownloadWithInfo(ctx, url, jobDir, info)
}

func (p *PipelineDownloader) DownloadWithInfo(ctx context.Context, url string, jobDir string, info *MediaInfo) (*DownloadResult, error) {
	u := NormalizeURL(url)

	if p.Semaphore != nil {
		p.Semaphore.Acquire()
		defer p.Semaphore.Release()
	}

	cacheKey := HashURL(u)
	if p.Cache.Root != "" {
		if files, ok := p.Cache.Has(cacheKey); ok {
			return &DownloadResult{Files: files, Size: fileTotalSize(files)}, nil
		}
	}

	// Detection failure should not block downloads completely (fallback to URL heuristics).
	if info == nil {
		detected, derr := p.Detector.Detect(ctx, u)
		if derr != nil {
			p.logf("[detect] url=%s err=%v", u, derr)
		}
		info = detected
	}

	strat := p.Registry.StrategyFor(info, u)
	engines := strat.EnginesFor(info)
	optsMatrix := strat.OptionsMatrix(u)
	if info != nil {
		p.logf("[job] platform=%s type=%s", info.Platform, info.Type)
	} else {
		p.logf("[job] platform=%s type=%s", PlatformFromURL(u), "unknown")
	}

	var lastErr error
	for _, engine := range engines {
		for idx, opts := range optsMatrix {
			engineName := engine.Name()
			attemptLabel := idx + 1

			p.logf("[download] engine=%s retry=%d url=%s", engineName, attemptLabel, u)

			res, err := engine.Download(ctx, u, jobDir, opts)
			if err == nil && res != nil && len(res.Files) > 0 {
				// Cache on success.
				if p.Cache.Root != "" {
					if cachedFiles, cerr := p.Cache.Save(cacheKey, res.Files); cerr != nil {
						// Cache failures should not fail the download itself.
						p.logf("[cache] save_failed key=%s err=%v", cacheKey, cerr)
					} else if len(cachedFiles) > 0 {
						p.logf("[download] engine=%s status=success cache=hit", engineName)
					}
				}

				p.logf("[download] engine=%s status=success", engineName)
				// Prefer cached files if present; otherwise return job dir output.
				if p.Cache.Root != "" {
					if files, ok := p.Cache.Has(cacheKey); ok {
						return &DownloadResult{Files: files, Size: fileTotalSize(files)}, nil
					}
				}
				files := allFilesInDir(jobDir)
				return &DownloadResult{Files: files, Size: fileTotalSize(files)}, nil
			}

			if err == nil {
				err = fmt.Errorf("%s produced empty result", engineName)
			}
			lastErr = err
			p.logf("[download] engine=%s status=fail err=%v", engineName, err)
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("download failed")
	}
	return nil, lastErr
}

func (p *PipelineDownloader) EnsureDirs() error {
	root := p.DownloadsRoot
	if root == "" {
		root = "downloads"
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	if p.Cache.Root != "" {
		if err := os.MkdirAll(p.Cache.Root, 0755); err != nil {
			return err
		}
	}
	return nil
}

func (p *PipelineDownloader) CacheRootDefault() {
	root := p.DownloadsRoot
	if root == "" {
		root = "downloads"
	}
	if p.Cache.Root == "" {
		p.Cache.Root = filepath.Join(root, "cache")
	}
}

func fileTotalSize(files []string) int64 {
	var sum int64
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && !st.IsDir() {
			sum += st.Size()
		}
	}
	return sum
}

func allFilesInDir(dir string) []string {
	var files []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			// ignore cache dir inside jobDir (shouldn't exist, but be safe)
			if strings.Contains(path, string(os.PathSeparator)+"cache"+string(os.PathSeparator)) {
				return nil
			}
			files = append(files, path)
		}
		return nil
	})
	// deterministic order
	// small; OK to sort with strings
	sortStrings(files)
	return files
}

func sortStrings(s []string) {
	if len(s) < 2 {
		return
	}
	// simple insertion sort to avoid importing sort in this file
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

