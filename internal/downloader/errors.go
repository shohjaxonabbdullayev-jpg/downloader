package downloader

import "errors"

var (
	ErrUnsupported = errors.New("unsupported url")
	ErrPrivate     = errors.New("private or login-required content")
	ErrNotFound    = errors.New("content not found")
)

