package downloader

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
)

func NewJobID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func NewJobDir(root string) (jobID string, jobDir string, err error) {
	if root == "" {
		root = "downloads"
	}
	jobID = NewJobID()
	jobDir = filepath.Join(root, "job_"+jobID)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		return "", "", err
	}
	return jobID, jobDir, nil
}

