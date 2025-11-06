package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ffmpegPath     = "/usr/bin/ffmpeg"
	ytDlpPath      = "yt-dlp"
	maxVideoHeight = 720 // limit YouTube/other videos
)

var (
	downloadsDir  = "downloads"
	instagramFile = "instagram.txt"
	pinterestFile = "pinterest.txt"
	sem           = make(chan struct{}, 3) // concurrency limit
)

func main() {
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set")
	}

	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("❌ Failed to create downloads folder: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Bot init failed: %v", err)
	}
	log.Printf("🤖 Bot authorized as @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}
	}
}

// ---------------- MESSAGE HANDLER ----------------
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	chatID := msg.Chat.ID

	switch text {
	case "/start":
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("👋 Salom %s!\n🎥 Menga YouTube, Instagram, TikTok yoki Pinterest link yuboring.", msg.From.FirstName)))
		return
	case "/help":
		bot.Send(tgbotapi.NewMessage(chatID, "❓ Yordam: @nonfindable1"))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda...")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, mediaType, err := downloadMedia(url)
			<-sem

			// delete loading message
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: loadingMsgID})

			if err != nil {
				bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Yuklab bo‘lmadi: %v", err)))
				return
			}

			for _, f := range files {
				sendMedia(bot, chatID, f, replyToID, mediaType)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// ---------------- LINK EXTRACTION ----------------
func extractSupportedLinks(text string) []string {
	re := regexp.MustCompile(`https?://[^\s]+`)
	matches := re.FindAllString(text, -1)
	var links []string
	for _, m := range matches {
		if strings.Contains(m, "youtube.com") || strings.Contains(m, "youtu.be") ||
			strings.Contains(m, "instagram.com") || strings.Contains(m, "tiktok.com") ||
			strings.Contains(m, "pinterest.com") {
			links = append(links, m)
		}
	}
	return links
}

// ---------------- DOWNLOAD MEDIA ----------------
func downloadMedia(link string) ([]string, string, error) {
	start := time.Now()
	if strings.Contains(link, "youtube.com") || strings.Contains(link, "youtu.be") {
		return downloadYouTubeRapidAPI(link, start)
	}
	if strings.Contains(link, "instagram.com") {
		return downloadYTLP(link, instagramFile, start)
	}
	if strings.Contains(link, "tiktok.com") {
		return downloadYTLP(link, "", start)
	}
	if strings.Contains(link, "pinterest.com") {
		return downloadPinterest(link, start)
	}
	return nil, "", fmt.Errorf("unsupported link")
}

// ---------------- YOUTUBE via RapidAPI ----------------
type RapidAPIResponse struct {
	Success bool   `json:"success"`
	Content string `json:"content"`
	Title   string `json:"title"`
}

func downloadYouTubeRapidAPI(videoURL string, start time.Time) ([]string, string, error) {
	apiKey := os.Getenv("RAPIDAPI_KEY")
	if apiKey == "" {
		return nil, "", fmt.Errorf("RAPIDAPI_KEY not set")
	}

	reqURL := fmt.Sprintf("https://youtube-info-download-api.p.rapidapi.com/ajax/download.php?format=mp4&add_info=0&url=%s",
		url.QueryEscape(videoURL),
	)
	req, _ := http.NewRequest("GET", reqURL, nil)
	req.Header.Set("x-rapidapi-host", "youtube-info-download-api.p.rapidapi.com")
	req.Header.Set("x-rapidapi-key", apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var apiResp RapidAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, "", err
	}
	if !apiResp.Success {
		return nil, "", fmt.Errorf("API returned failure")
	}

	htmlBytes, err := base64.StdEncoding.DecodeString(apiResp.Content)
	if err != nil {
		return nil, "", err
	}
	html := string(htmlBytes)

	// extract mp4 URL
	re := regexp.MustCompile(`https?://[^\s"'<>]+\.mp4`)
	match := re.FindString(html)
	if match == "" {
		return nil, "", fmt.Errorf("video URL not found in API response")
	}

	// download actual video
	filename := fmt.Sprintf("%s/%d_youtube.mp4", downloadsDir, time.Now().Unix())
	out, err := os.Create(filename)
	if err != nil {
		return nil, "", err
	}
	defer out.Close()

	videoResp, err := client.Get(match)
	if err != nil {
		return nil, "", err
	}
	defer videoResp.Body.Close()

	_, err = io.Copy(out, videoResp.Body)
	if err != nil {
		return nil, "", err
	}

	return []string{filename}, "video", nil
}

// ---------------- YT-DLP Downloader (Instagram/TikTok) ----------------
func downloadYTLP(link, cookieFile string, start time.Time) ([]string, string, error) {
	args := []string{"--no-warnings", "-f", fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best", maxVideoHeight), "-o", filepath.Join(downloadsDir, "%(title)s.%(ext)s"), link}
	if cookieFile != "" && fileExists(cookieFile) {
		args = append(args, "--cookies", cookieFile)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	if err != nil {
		return nil, "", fmt.Errorf("yt-dlp error: %v\n%s", err, out)
	}
	files := filesCreatedAfter(downloadsDir, start)
	mediaType := "video"
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".jpg" || ext == ".png" {
			mediaType = "image"
			break
		}
	}
	return files, mediaType, nil
}

// ---------------- PINTEREST ----------------
func downloadPinterest(link string, start time.Time) ([]string, string, error) {
	args := []string{"-d", downloadsDir, link}
	out, err := runCommandCapture("gallery-dl", args...)
	if err != nil {
		return nil, "", fmt.Errorf("gallery-dl error: %v\n%s", err, out)
	}
	files := filesCreatedAfter(downloadsDir, start)
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no files downloaded from Pinterest")
	}
	return files, "image", nil
}

// ---------------- HELPERS ----------------
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

func filesCreatedAfter(dir string, t time.Time) []string {
	var res []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, _ := d.Info()
		if info.ModTime().After(t) {
			res = append(res, path)
		}
		return nil
	})
	sort.Slice(res, func(i, j int) bool {
		fi, _ := os.Stat(res[i])
		fj, _ := os.Stat(res[j])
		return fi.ModTime().Before(fj.ModTime())
	})
	return res
}

// ---------------- SEND MEDIA ----------------
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) {
	caption := "@downloaderin123_bot orqali yuklab olindi"

	switch mediaType {
	case "video":
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.Caption = caption
		video.ReplyToMessageID = replyTo
		if _, err := bot.Send(video); err != nil {
			log.Printf("⚠️ Failed to send video: %v", err)
		}

	case "image":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.Caption = caption
		photo.ReplyToMessageID = replyTo
		if _, err := bot.Send(photo); err != nil {
			log.Printf("⚠️ Failed to send photo: %v", err)
		}
	}

	// Delete file after sending
	if err := os.Remove(filePath); err != nil {
		log.Printf("⚠️ Failed to delete file %s: %v", filePath, err)
	} else {
		log.Printf("🗑️ Deleted file %s after sending", filePath)
	}
}
