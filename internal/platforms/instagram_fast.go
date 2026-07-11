package platforms

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"telegram_bot_downloader/internal/execx"
	"telegram_bot_downloader/internal/model"
)

// FastInstagramEngine resolves an Instagram post with a SINGLE graphql/query
// request and downloads the CDN media — the same fast path as the native Go
// extractor, but made through curl_cffi's Chrome TLS impersonation.
//
// WHY curl_cffi and not plain Go/requests: from a flagged datacenter IP (Render)
// Instagram fingerprints the TLS handshake and returns an EMPTY response to
// Go's stdlib TLS and to Python-requests — only a real browser fingerprint
// (curl_cffi, the same thing yt-dlp's --impersonate uses) is served. So this is
// the fast path that actually works on the deploy. Handles reels, photos, and
// carousels; ~1s vs yt-dlp's ~3-15s cascade. Falls through on any failure.
//
// MAINTENANCE: Instagram rotates the graphql doc_id every ~2-4 weeks; when it
// does the response comes back empty and the pipeline falls back to yt-dlp —
// update igPolarisPostDocID to re-enable the fast path.
type FastInstagramEngine struct {
	// Python is a python with curl_cffi (e.g. the Docker /opt/yt venv). Empty =>
	// the built-in candidate list.
	Python string
}

func (FastInstagramEngine) Name() string { return "instagram-fast" }

// igFastScript resolves an IG post two ways (both over curl_cffi's Chrome TLS)
// and downloads the media into arg 2:
//  1. graphql PolarisPostRootQuery — fastest, works for most reels/videos.
//  2. parse the post page's embedded JSON — universal (photos, carousels, and
//     posts the graphql "execution error"s on) AND doc_id-independent, so it keeps
//     working when Instagram rotates the doc_id.
// Exit 3 = curl_cffi unavailable (try the next interpreter); 2 = a real failure
// (fall back to the next engine).
const igFastScript = `
import sys, os, json, re
try:
    from curl_cffi import requests
except Exception as e:
    print("NO_CURL_CFFI:", repr(e)); sys.exit(3)

shortcode, target = sys.argv[1], sys.argv[2]
s = requests.Session(impersonate="chrome")

def collect(item):
    if item.get("media_type") == 8 or item.get("carousel_media"):
        out = []
        for c in item.get("carousel_media") or []:
            out += collect(c)
        return out
    vv = item.get("video_versions") or []
    if vv:
        return [(vv[0]["url"], "mp4")]
    cands = ((item.get("image_versions2") or {}).get("candidates")) or []
    if cands:
        return [(cands[0]["url"], "jpg")]
    return []

item = None

# Method 1: graphql (fast path).
try:
    j = s.get("https://www.instagram.com/data/shared_data/", timeout=15).json()
    csrf = (j.get("config") or {}).get("csrf_token") or j.get("csrf_token") or ""
    if csrf:
        variables = json.dumps({"shortcode": shortcode,
            "__relay_internal__pv__PolarisAIGMMediaWebLabelEnabledrelayprovider": False})
        r = s.post("https://www.instagram.com/graphql/query",
            data={"doc_id": "` + igPolarisPostDocID + `", "variables": variables},
            headers={"X-IG-App-ID": "` + igAppID + `", "X-CSRFToken": csrf,
                     "X-Requested-With": "XMLHttpRequest",
                     "Referer": "https://www.instagram.com/p/%s/" % shortcode,
                     "Origin": "https://www.instagram.com"},
            timeout=15)
        items = ((r.json().get("data") or {}).get("xdt_api__v1__media__shortcode__web_info") or {}).get("items") or []
        if items:
            item = items[0]
except Exception:
    pass

# Method 2: parse the post page (universal + doc_id-independent).
if item is None:
    def find_items(obj, found):
        if isinstance(obj, dict):
            if ("video_versions" in obj or "image_versions2" in obj or "carousel_media" in obj) and ("code" in obj or "pk" in obj):
                found.append(obj)
            for v in obj.values():
                find_items(v, found)
        elif isinstance(obj, list):
            for v in obj:
                find_items(v, found)
        elif isinstance(obj, str):
            t = obj.strip()
            if t[:1] in "{[" and ("image_versions2" in t or "video_versions" in t or "carousel_media" in t):
                try:
                    find_items(json.loads(t), found)
                except Exception:
                    pass
    try:
        html = s.get("https://www.instagram.com/p/%s/" % shortcode, timeout=20).text
        found = []
        for m in re.finditer(r'<script type="application/json"[^>]*>(.*?)</script>', html, re.S):
            blob = m.group(1)
            if "image_versions2" in blob or "video_versions" in blob or "carousel_media" in blob:
                try:
                    find_items(json.loads(blob), found)
                except Exception:
                    pass
        match = [it for it in found if it.get("code") == shortcode]
        if match:
            item = match[0]
        elif len(found) == 1:
            item = found[0]
    except Exception as e:
        print("PAGE_ERROR:", repr(e))

if item is None:
    print("NO_MEDIA_FOUND"); sys.exit(2)

media = collect(item)
if not media:
    print("NO_MEDIA_URLS"); sys.exit(2)

for i, (url, ext) in enumerate(media):
    dst = os.path.join(target, "%s_%02d.%s" % (shortcode, i, ext))
    resp = s.get(url, timeout=60)
    if resp.status_code != 200:
        print("CDN_HTTP", resp.status_code); sys.exit(2)
    with open(dst, "wb") as f:
        f.write(resp.content)
print("OK", len(media))
`

func (e FastInstagramEngine) Download(ctx context.Context, url string, jobDir string, opts Options) (*model.DownloadResult, error) {
	shortcode := extractInstagramShortcode(url)
	if shortcode == "" {
		return nil, fmt.Errorf("instagram-fast: could not extract shortcode")
	}

	var lastOut string
	var lastErr error
	ran := false
	for _, candidate := range resolvePythons(e.Python) {
		res, err := execx.Run(ctx, candidate, "-c", igFastScript, shortcode, jobDir)
		out := strings.TrimSpace(res.Output)
		if err == nil {
			ran = true
			lastErr = nil
			break
		}
		lastErr, lastOut = err, out
		// curl_cffi missing on this interpreter -> try the next one; any other
		// failure is authoritative (this python ran the request), so stop.
		if !strings.Contains(out, "NO_CURL_CFFI") {
			break
		}
	}
	if !ran {
		if lastOut != "" {
			return nil, fmt.Errorf("instagram-fast: %s", firstLine(lastOut))
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("instagram-fast: no usable python")
	}

	files := allFiles(jobDir)
	if len(files) == 0 {
		return nil, fmt.Errorf("instagram-fast: produced no files")
	}
	return &model.DownloadResult{Files: files, Size: totalSize(files)}, nil
}

// resolvePythons returns the python interpreters to try, existence-filtered. The
// Docker venv (/opt/yt) is first because that's where curl_cffi/instaloader live.
func resolvePythons(override string) []string {
	var candidates []string
	if strings.TrimSpace(override) != "" {
		candidates = []string{override}
	} else {
		candidates = []string{
			"/opt/yt/bin/python3", "/opt/yt/bin/python",
			"/usr/bin/python3", "/usr/local/bin/python3", "python3", "python",
		}
	}
	var out []string
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil && p != "" {
			out = append(out, c)
		}
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
