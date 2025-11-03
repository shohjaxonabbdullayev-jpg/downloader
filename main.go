package main

import (
	"bytes"
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
	ffmpegPath = "/usr/bin/ffmpeg"
	ytDlpPath  = "yt-dlp"
)

var (
	downloadsDir  = "downloads"
	instagramFile = "instagram.txt"
	pinterestFile = "pinterest.txt"
	sem           = make(chan struct{}, 3)
)

// ===========================================================
//
//	MAIN
//
// ===========================================================
func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env file not found, using system environment")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	go startHealthCheckServer(port)

	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("❌ Failed to create downloads folder: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Bot initialization failed: %v", err)
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

// ===========================================================
//
//	HEALTH CHECK SERVER
//
// ===========================================================
func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "OK")
	})
	log.Printf("💚 Health check server running on port %s", port)
	http.ListenAndServe(":"+port, nil)
}

// ===========================================================
//
//	MESSAGE HANDLER
//
// ===========================================================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	chatID := msg.Chat.ID

	if text == "/start" {
		startMsg := fmt.Sprintf(
			"👋 Salom %s!\n\n🎥 Menga YouTube, Instagram yoki Pinterest link yuboring — men sizga videoni yoki rasmni yuboraman.",
			msg.From.FirstName,
		)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	}
	if text == "/help" {
		bot.Send(tgbotapi.NewMessage(chatID, "❓ Yordam uchun @nonfindable1 bilan bog‘laning."))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda... iltimos kuting.")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, mediaType, err := downloadMedia(url)
			<-sem

			// delete loading message
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: loadingMsgID})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				errorMsg := tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Yuklab bo‘lmadi: %v", err))
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			for _, file := range files {
				sendMediaAndAttachShareButtons(bot, chatID, file, replyToID, mediaType)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// ===========================================================
//
//	LINK EXTRACTION
//
// ===========================================================
func extractSupportedLinks(text string) []string {
	regex := `(https?://[^\s]+)`
	matches := regexp.MustCompile(regex).FindAllString(text, -1)
	var links []string
	for _, m := range matches {
		if isSupportedLink(m) {
			links = append(links, m)
		}
	}
	return links
}

func isSupportedLink(text string) bool {
	text = strings.ToLower(text)
	return strings.Contains(text, "youtube.com") ||
		strings.Contains(text, "youtu.be") ||
		strings.Contains(text, "instagram.com") ||
		strings.Contains(text, "instagr.am") ||
		strings.Contains(text, "pinterest.com") ||
		strings.Contains(text, "pin.it")
}

// ===========================================================
//
//	DOWNLOAD MEDIA
//
// ===========================================================
func downloadMedia(url string) ([]string, string, error) {
	start := time.Now()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().UnixNano()))

	switch {
	case strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be"):
		return downloadYouTubeRapidAPI(url)
	case strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am"):
		return downloadInstagram(url, outputTemplate, start)
	case strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it"):
		return downloadPinterest(url, outputTemplate, start)
	default:
		return nil, "", fmt.Errorf("unsupported link")
	}
}

// ===========================================================
//
//	YOUTUBE (USING yt-api.p.rapidapi.com)
//
// ===========================================================
func downloadYouTubeRapidAPI(videoURL string) ([]string, string, error) {
	apiKey := os.Getenv("RAPIDAPI_KEY")
	if apiKey == "" {
		return nil, "", fmt.Errorf("RAPIDAPI_KEY not set")
	}

	// 🧠 Handle YouTube Clips
	if strings.Contains(videoURL, "/clip/") {
		// Extract video ID pattern: https://youtube.com/clip/<clip_id>
		resolveURL := fmt.Sprintf("https://yt-api.p.rapidapi.com/resolve?url=%s", url.QueryEscape(videoURL))
		req, _ := http.NewRequest("GET", resolveURL, nil)
		req.Header.Add("x-rapidapi-host", "yt-api.p.rapidapi.com")
		req.Header.Add("x-rapidapi-key", apiKey)

		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("failed to resolve clip: %v", err)
		}
		defer res.Body.Close()

		var data map[string]interface{}
		json.NewDecoder(res.Body).Decode(&data)

		if originalURL, ok := data["videoUrl"].(string); ok && strings.Contains(originalURL, "youtube.com/watch") {
			log.Printf("🎬 Clip redirected to base video: %s", originalURL)
			videoURL = originalURL
		} else {
			return nil, "", fmt.Errorf("couldn't resolve clip to original video")
		}
	}

	// 🧩 Step 1: Call API
	apiEndpoint := fmt.Sprintf("https://yt-api.p.rapidapi.com/resolve?url=%s", url.QueryEscape(videoURL))
	req, err := http.NewRequest("GET", apiEndpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Add("x-rapidapi-host", "yt-api.p.rapidapi.com")
	req.Header.Add("x-rapidapi-key", apiKey)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("API request failed: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		return nil, "", fmt.Errorf("API error: %s", string(body))
	}

	// 🧩 Step 2: Parse JSON
	var data map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		return nil, "", fmt.Errorf("JSON parse error: %v", err)
	}

	// 🧩 Step 3: Extract downloadable URL
	videoURLFound := ""
	if formats, ok := data["formats"].([]interface{}); ok {
		for _, f := range formats {
			format := f.(map[string]interface{})
			if fmtStr, ok := format["url"].(string); ok && strings.Contains(fmtStr, ".googlevideo.com") {
				videoURLFound = fmtStr
				break
			}
		}
	}

	if videoURLFound == "" {
		return nil, "", fmt.Errorf("no downloadable MP4 URL found")
	}

	// 🧩 Step 4: Download file
	filePath := filepath.Join(downloadsDir, fmt.Sprintf("%d_youtube.mp4", time.Now().UnixNano()))
	out, err := os.Create(filePath)
	if err != nil {
		return nil, "", err
	}
	defer out.Close()

	resp, err := http.Get(videoURLFound)
	if err != nil {
		return nil, "", fmt.Errorf("download failed: %v", err)
	}
	defer resp.Body.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("file write failed: %v", err)
	}

	return []string{filePath}, "video", nil
}

// ===========================================================
//
//	INSTAGRAM & PINTEREST (yt-dlp + gallery-dl)
//
// ===========================================================
func downloadInstagram(url, output string, start time.Time) ([]string, string, error) {
	args := []string{"--no-warnings", "--ffmpeg-location", ffmpegPath, "-o", output, url}
	if fileExists(instagramFile) {
		args = append(args, "--cookies", instagramFile)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 Instagram yt-dlp output:\n%s", out)
	if err != nil {
		return nil, "", err
	}

	files := filesCreatedAfterRecursive(downloadsDir, start)
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no files found")
	}

	mediaType := "image"
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" {
			mediaType = "video"
			break
		}
	}
	return files, mediaType, nil
}

func downloadPinterest(url, output string, start time.Time) ([]string, string, error) {
	args := []string{"--no-warnings", "--ffmpeg-location", ffmpegPath, "-o", output, url}
	if fileExists(pinterestFile) {
		args = append(args, "--cookies", pinterestFile)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 Pinterest yt-dlp output:\n%s", out)
	files := filesCreatedAfterRecursive(downloadsDir, start)
	if err == nil && len(files) > 0 {
		return files, "video", nil
	}
	// fallback: gallery-dl
	argsGD := []string{"-d", downloadsDir, url}
	if fileExists(pinterestFile) {
		argsGD = []string{"--cookies", pinterestFile, "-d", downloadsDir, url}
	}
	out, err = runCommandCapture("gallery-dl", argsGD...)
	log.Printf("🖼️ Pinterest gallery-dl output:\n%s", out)
	files = filesCreatedAfterRecursive(downloadsDir, start)
	if err != nil || len(files) == 0 {
		return nil, "", fmt.Errorf("Pinterest download failed: %v", err)
	}
	return files, "image", nil
}

// ===========================================================
//
//	HELPERS
//
// ===========================================================
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func filesCreatedAfterRecursive(dir string, t time.Time) []string {
	var res []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			info, _ := d.Info()
			if info.ModTime().After(t) {
				res = append(res, path)
			}
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

// ===========================================================
//
//	SEND MEDIA + SHARE BUTTONS
//
// ===========================================================
func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) error {
	var sentMsg tgbotapi.Message
	var err error

	caption := "@downloaderin123_bot orqali yuklab olindi"

	if mediaType == "video" {
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.Caption = caption
		video.ReplyToMessageID = replyTo
		sentMsg, err = bot.Send(video)
	} else {
		img := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		img.Caption = caption
		img.ReplyToMessageID = replyTo
		sentMsg, err = bot.Send(img)
	}
	if err != nil {
		return err
	}

	msgLink := fmt.Sprintf("https://t.me/%s/%d", bot.Self.UserName, sentMsg.MessageID)
	shareURL := fmt.Sprintf("https://t.me/share/url?url=%s", url.QueryEscape(msgLink))
	btnShare := tgbotapi.NewInlineKeyboardButtonURL("📤 Ulashish", shareURL)
	btnGroup := tgbotapi.NewInlineKeyboardButtonURL(
		"👥 Guruhga qo‘shish",
		fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName),
	)
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(btnShare),
		tgbotapi.NewInlineKeyboardRow(btnGroup),
	)
	edit := tgbotapi.NewEditMessageReplyMarkup(chatID, sentMsg.MessageID, keyboard)
	bot.Send(edit)
	return nil
}
