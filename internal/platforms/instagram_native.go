package platforms

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"telegram_bot_downloader/internal/model"
)

// NativeInstagramEngine resolves an Instagram post's media with a SINGLE
// graphql/query request and downloads the direct CDN URLs itself — no yt-dlp
// subprocess and, crucially, no datacenter "failed-request cascade" (yt-dlp's IG
// extractor fires 3-5 sequential requests + a ~600KB HTML fetch when the api/v1
// endpoints get soft-blocked on a cloud IP, which is the bulk of the ~3.3s).
//
// Measured (live): ~0.7s cold / ~0.25s warm to resolve, vs yt-dlp's ~3.3s. It
// handles reels (video), single photos, and carousels. On ANY failure it returns
// an error so the pipeline falls back to yt-dlp / instaloader — so the bot never
// breaks, it just gets slower until the native path works again.
//
// MAINTENANCE: Instagram rotates the doc_id every ~2-4 weeks as anti-scraping
// (it just moved 8845758582119845 -> 27128499623469141). When it rotates, the
// graphql response comes back empty and we fall back automatically; update
// igPolarisPostDocID to re-enable the fast path.
type NativeInstagramEngine struct{}

func (NativeInstagramEngine) Name() string { return "instagram-native" }

const (
	// PolarisPostRootQuery doc_id (see MAINTENANCE note above).
	igPolarisPostDocID = "27128499623469141"
	// Public web-client app id Instagram's own frontend sends.
	igAppID = "936619743392459"
)

var (
	igOnce   sync.Once
	igClient *http.Client

	igCSRFMu sync.Mutex
	igCSRF   string
	igCSRFAt time.Time
)

// igHTTP returns a shared, keep-alive HTTP client (connection pooling + a cookie
// jar) so warm requests skip the TLS handshake and reuse the csrf/mid cookies.
func igHTTP() *http.Client {
	igOnce.Do(func() {
		jar, _ := cookiejar.New(nil)
		igClient = &http.Client{
			Timeout: 20 * time.Second,
			Jar:     jar,
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		}
	})
	return igClient
}

// WarmInstagram pre-fetches the CSRF token and warms the connection pool at
// startup, so the first real user request doesn't pay the ~2s cold preflight.
// Safe to call in a goroutine; errors are ignored (the first request just falls
// back to a lazy fetch).
func WarmInstagram() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = igCSRFToken(ctx)
}

// igCSRFToken returns a cached csrftoken, fetching one via a cheap preflight when
// missing/stale. Instagram accepts the token for a long time, so after the first
// call this is off the per-request critical path.
func igCSRFToken(ctx context.Context) (string, error) {
	igCSRFMu.Lock()
	if igCSRF != "" && time.Since(igCSRFAt) < 30*time.Minute {
		t := igCSRF
		igCSRFMu.Unlock()
		return t, nil
	}
	igCSRFMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.instagram.com/data/shared_data/", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("X-IG-App-ID", igAppID)
	resp, err := igHTTP().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))

	// The token appears either at config.csrf_token or top-level csrf_token.
	var sd struct {
		CSRFToken string `json:"csrf_token"`
		Config    struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"config"`
	}
	_ = json.Unmarshal(body, &sd)
	token := sd.Config.CSRFToken
	if token == "" {
		token = sd.CSRFToken
	}
	if token == "" {
		return "", fmt.Errorf("instagram-native: no csrf token in shared_data")
	}
	igCSRFMu.Lock()
	igCSRF, igCSRFAt = token, time.Now()
	igCSRFMu.Unlock()
	return token, nil
}

// igCandidate is one media rendition (video or image).
type igCandidate struct {
	URL string `json:"url"`
}

// igItem is one media item; media_type 1=image, 2=video, 8=carousel.
type igItem struct {
	MediaType      int           `json:"media_type"`
	VideoVersions  []igCandidate `json:"video_versions"`
	ImageVersions2 struct {
		Candidates []igCandidate `json:"candidates"`
	} `json:"image_versions2"`
	CarouselMedia []igItem `json:"carousel_media"`
}

type igResp struct {
	Data struct {
		WebInfo struct {
			Items []igItem `json:"items"`
		} `json:"xdt_api__v1__media__shortcode__web_info"`
	} `json:"data"`
}

type igMedia struct {
	url     string
	isVideo bool
}

// collectIGMedia flattens a post item into a downloadable URL list (videos
// preferred over their thumbnails; every carousel entry included).
func collectIGMedia(item igItem) []igMedia {
	var out []igMedia
	switch item.MediaType {
	case 8: // carousel / sidecar
		for _, c := range item.CarouselMedia {
			out = append(out, collectIGMedia(c)...)
		}
		return out
	case 2: // video
		if len(item.VideoVersions) > 0 {
			return []igMedia{{url: item.VideoVersions[0].URL, isVideo: true}}
		}
	}
	// image (type 1) or a video item that only exposed images
	if len(item.VideoVersions) > 0 {
		return []igMedia{{url: item.VideoVersions[0].URL, isVideo: true}}
	}
	if len(item.ImageVersions2.Candidates) > 0 {
		return []igMedia{{url: item.ImageVersions2.Candidates[0].URL, isVideo: false}}
	}
	return out
}

func (e NativeInstagramEngine) Download(ctx context.Context, rawURL string, jobDir string, opts Options) (*model.DownloadResult, error) {
	shortcode := extractInstagramShortcode(rawURL)
	if shortcode == "" {
		return nil, fmt.Errorf("instagram-native: could not extract shortcode")
	}

	token, err := igCSRFToken(ctx)
	if err != nil {
		return nil, err
	}

	vars, _ := json.Marshal(map[string]any{
		"shortcode": shortcode,
		"__relay_internal__pv__PolarisAIGMMediaWebLabelEnabledrelayprovider": false,
	})
	form := url.Values{}
	form.Set("doc_id", igPolarisPostDocID)
	form.Set("variables", string(vars))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://www.instagram.com/graphql/query", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("X-IG-App-ID", igAppID)
	req.Header.Set("X-CSRFToken", token)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", "https://www.instagram.com/p/"+shortcode+"/")
	req.Header.Set("Origin", "https://www.instagram.com")

	resp, err := igHTTP().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("instagram-native: graphql http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	var r igResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("instagram-native: decode: %w", err)
	}
	items := r.Data.WebInfo.Items
	if len(items) == 0 {
		// doc_id rotated, or login-required/private from this IP -> fall back.
		return nil, fmt.Errorf("instagram-native: empty web_info (doc_id may have rotated or login required)")
	}
	media := collectIGMedia(items[0])
	if len(media) == 0 {
		return nil, fmt.Errorf("instagram-native: no downloadable media in response")
	}

	var files []string
	for i, m := range media {
		ext := ".jpg"
		if m.isVideo {
			ext = ".mp4"
		}
		dst := filepath.Join(jobDir, fmt.Sprintf("%s_%02d%s", shortcode, i, ext))
		if err := igDownloadTo(ctx, m.url, dst); err != nil {
			// A partial carousel is worse than a clean fallback.
			return nil, fmt.Errorf("instagram-native: download item %d: %w", i, err)
		}
		files = append(files, dst)
	}
	return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
}

// igDownloadTo fetches a direct CDN URL to disk over the shared keep-alive client.
func igDownloadTo(ctx context.Context, mediaURL, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := igHTTP().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cdn http %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}
