package model

type MediaInfo struct {
	Platform   string
	Type       string // video, image, carousel, unknown
	Duration   int
	Size       int64
	Title      string
	WebpageURL string
	Uploader   string
	Thumbnail  string
}

type DownloadResult struct {
	Files []string
	Size  int64
}

