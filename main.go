package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ffmpegPath     = "/usr/bin/ffmpeg"
	ytDlpPath      = "yt-dlp"
	maxYouTubeP    = 720 // Force YouTube to download <= 720p
	maxVideoHeight = 720 // General maximum height for videos we ask yt-dlp for
)

var (
	downloadsDir  = "downloads"
	instagramFile = "instagram.txt"
	youtubeFile   = "youtube.txt"
	pinterestFile = "pinterest.txt"
	sem           = make(chan struct{}, 3) // concurrent downloads limit
)

// -------------------- MAIN --------------------
func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env file not found — using environment variables")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// create downloads dir
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("❌ Failed to create downloads dir: %v", err)
	}

	// start health check
	go startHealthCheckServer(port)

	// init bot
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Failed to init bot: %v", err)
	}
	log.Printf("🤖 Bot authorized as @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}
		// handle in goroutine
		go handleMessage(bot, update.Message)
	}
}

// -------------------- HEALTH CHECK --------------------
func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "OK")
	})
	log.Printf("💚 Health server listening on :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Health server error: %v", err)
	}
}

// -------------------- MESSAGE HANDLER --------------------
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	chatID := msg.Chat.ID

	switch text {
	case "/start":
		startMsg := fmt.Sprintf("👋 Salom %s!\n\n🎥 Menga YouTube, Instagram, TikTok yoki Pinterest link yuboring — men sizga videoni yoki rasmni yuboraman.", msg.From.FirstName)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	case "/help":
		helpMsg := "❓ Yordam uchun @nonfindable1 ga yozing."
		bot.Send(tgbotapi.NewMessage(chatID, helpMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		// no supported links — optionally you can send a reply
		return
	}

	for _, link := range links {
		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda... iltimos kuting.")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		// spawn goroutine per link with concurrency semaphore
		go func(link string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, mediaType, err := downloadMedia(link)
			<-sem

			// delete loading message (best-effort)
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", link, err)
				errorMsg := tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Yuklab bo'lmadi: %v", err))
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			for _, f := range files {
				if err := sendMediaAndAttachShareButtons(bot, chatID, f, replyToID, mediaType); err != nil {
					log.Printf("⚠️ Failed to send %s: %v", f, err)
				}
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// -------------------- LINK EXTRACTION --------------------
func extractSupportedLinks(text string) []string {
	regex := `(https?://[^\s]+)`
	matches := regexp.MustCompile(regex).FindAllString(text, -1)
	var out []string
	for _, m := range matches {
		// strip trailing punctuation
		m = strings.TrimRight(m, ".,;!?)")
		if isSupportedLink(m) {
			out = append(out, m)
		}
	}
	return out
}

func isSupportedLink(text string) bool {
	l := strings.ToLower(text)
	return strings.Contains(l, "youtube.com") ||
		strings.Contains(l, "youtu.be") ||
		strings.Contains(l, "instagram.com") ||
		strings.Contains(l, "instagr.am") ||
		strings.Contains(l, "pinterest.com") ||
		strings.Contains(l, "pin.it") ||
		strings.Contains(l, "tiktok.com") ||
		strings.Contains(l, "vm.tiktok.com")
}

// -------------------- DOWNLOAD MEDIA (dispatcher) --------------------
func downloadMedia(link string) ([]string, string, error) {
	start := time.Now()
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	switch {
	case strings.Contains(link, "youtube.com") || strings.Contains(link, "youtu.be"):
		return downloadYouTube(link, outputTemplate, start)
	case strings.Contains(link, "instagram.com") || strings.Contains(link, "instagr.am"):
		return downloadInstagram(link, outputTemplate, start)
	case strings.Contains(link, "pinterest.com") || strings.Contains(link, "pin.it"):
		return downloadPinterest(link, outputTemplate, start)
	case strings.Contains(link, "tiktok.com") || strings.Contains(link, "vm.tiktok.com"):
		return downloadTikTok(link, outputTemplate, start)
	default:
		return nil, "", fmt.Errorf("unsupported link")
	}
}

// -------------------- YOUTUBE --------------------
func downloadYouTube(link, output string, start time.Time) ([]string, string, error) {
	// Force yt-dlp to download best video <= 720p + best audio, fallback to best[height<=720]
	formatSelector := fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best[height<=%d]", maxYouTubeP, maxYouTubeP)
	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--restrict-filenames",
		"--ffmpeg-location", ffmpegPath,
		"-f", formatSelector,
		"--merge-output-format", "mp4",
		"-o", output,
		link,
	}
	if fileExists(youtubeFile) {
		args = append(args, "--cookies", youtubeFile)
	}

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp (YouTube) output:\n%s", out)

	// detect cookie expiration hints
	lcOut := strings.ToLower(out)
	if strings.Contains(lcOut, "login required") || strings.Contains(lcOut, "cookies") || strings.Contains(lcOut, "expired") {
		log.Println("⚠️ YouTube cookies might be expired — please update youtube.txt")
		adminChat := os.Getenv("ADMIN_CHAT_ID")
		if adminChat != "" {
			notifyAdmin(adminChat, "⚠️ YouTube cookies expired! Please update youtube.txt in the server.")
		}
	}

	files := filesCreatedAfterRecursive(downloadsDir, start)
	return files, "video", err
}

// -------------------- INSTAGRAM --------------------
func downloadInstagram(link, output string, start time.Time) ([]string, string, error) {
	args := []string{
		"--no-warnings",
		"--ffmpeg-location", ffmpegPath,
		"-o", output,
		link,
	}
	if fileExists(instagramFile) {
		args = append(args, "--cookies", instagramFile)
	}

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp (Instagram) output:\n%s", out)
	if err != nil {
		return nil, "", fmt.Errorf("yt-dlp error: %v", err)
	}

	files := filesCreatedAfterRecursive(downloadsDir, start)
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no files downloaded from Instagram")
	}

	mediaType := "image"
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" {
			mediaType = "video"
			break
		}
	}
	return files, mediaType, nil
}

// -------------------- PINTEREST --------------------
func downloadPinterest(link, output string, start time.Time) ([]string, string, error) {
	// Try yt-dlp first (works for many Pinterest videos)
	args := []string{
		"--no-warnings",
		"--ffmpeg-location", ffmpegPath,
		"-o", output,
		link,
	}
	if fileExists(pinterestFile) {
		args = append(args, "--cookies", pinterestFile)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp (Pinterest) output:\n%s", out)

	files := filesCreatedAfterRecursive(downloadsDir, start)
	if err == nil && len(files) > 0 {
		// try to reduce height if needed
		for _, f := range files {
			ext := strings.ToLower(filepath.Ext(f))
			if ext == ".mp4" || ext == ".mov" {
				// scale down to maxVideoHeight (best-effort, non-blocking)
				tmp := f + "_tmp.mp4"
				_ = exec.Command(ffmpegPath, "-i", f, "-vf", fmt.Sprintf("scale=-2:%d", maxVideoHeight), "-c:a", "copy", tmp).Run()
				_ = os.Rename(tmp, f)
			}
		}
		return files, "video", nil
	}

	// Fallback: gallery-dl for images
	argsGD := []string{"-d", downloadsDir, link}
	if fileExists(pinterestFile) {
		argsGD = []string{"--cookies", pinterestFile, "-d", downloadsDir, link}
	}
	out2, err2 := runCommandCapture("gallery-dl", argsGD...)
	log.Printf("🖼️ gallery-dl (Pinterest) output:\n%s", out2)
	files = filesCreatedAfterRecursive(downloadsDir, start)
	if err2 != nil || len(files) == 0 {
		return nil, "", fmt.Errorf("Pinterest download failed: %v / %v", err, err2)
	}

	// Determine type
	mediaType := "image"
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" {
			mediaType = "video"
			break
		}
	}
	return files, mediaType, nil
}

// -------------------- TIKTOK --------------------
func downloadTikTok(link, output string, start time.Time) ([]string, string, error) {
	// TikTok doesn't need cookies in most cases
	args := []string{
		"--no-warnings",
		"--restrict-filenames",
		"--ffmpeg-location", ffmpegPath,
		"-f", fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best", maxVideoHeight),
		"--merge-output-format", "mp4",
		"-o", output,
		link,
	}

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp (TikTok) output:\n%s", out)
	if err != nil {
		return nil, "", fmt.Errorf("yt-dlp error: %v", err)
	}

	files := filesCreatedAfterRecursive(downloadsDir, start)
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no files downloaded from TikTok")
	}
	return files, "video", nil
}

// -------------------- HELPERS --------------------
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

func filesCreatedAfterRecursive(dir string, t time.Time) []string {
	var res []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
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

func notifyAdmin(chatID string, msg string) {
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Println("⚠️ BOT_TOKEN missing, cannot notify admin")
		return
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Printf("⚠️ Failed to init bot for admin notify: %v", err)
		return
	}

	chat, err := strconv.ParseInt(chatID, 10, 64)
	if err != nil {
		log.Printf("⚠️ Invalid ADMIN_CHAT_ID: %v", err)
		return
	}
	bot.Send(tgbotapi.NewMessage(chat, msg))
}

// -------------------- SENDING MEDIA, ATTACH KEYBOARD, DELETE FILE --------------------
func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) error {
	var sentMsg tgbotapi.Message
	var err error

	caption := "@downloaderin123_bot orqali yuklab olindi"

	switch mediaType {
	case "video":
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.ReplyToMessageID = replyTo
		video.Caption = caption
		sentMsg, err = bot.Send(video)
	case "image":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.ReplyToMessageID = replyTo
		photo.Caption = caption
		sentMsg, err = bot.Send(photo)
	default:
		// attempt to send as document
		doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
		doc.ReplyToMessageID = replyTo
		doc.Caption = caption
		sentMsg, err = bot.Send(doc)
	}

	if err != nil {
		return fmt.Errorf("failed to send media: %w", err)
	}

	// attach share & group buttons
	msgLink := fmt.Sprintf("https://t.me/%s/%d", bot.Self.UserName, sentMsg.MessageID)
	shareURL := fmt.Sprintf("https://t.me/share/url?url=%s", url.QueryEscape(msgLink))

	btnShare := tgbotapi.NewInlineKeyboardButtonURL("📤 Do'stlar bilan ulashish", shareURL)
	btnGroup := tgbotapi.NewInlineKeyboardButtonURL("👥 Guruhga qo'shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(btnShare),
		tgbotapi.NewInlineKeyboardRow(btnGroup),
	)

	// best-effort to attach keyboard (edit reply markup)
	if _, err := bot.Send(tgbotapi.NewEditMessageReplyMarkup(chatID, sentMsg.MessageID, keyboard)); err != nil {
		log.Printf("⚠️ Failed to attach keyboard to message %d: %v", sentMsg.MessageID, err)
	}

	// delete file after sending (best-effort)
	if err := os.Remove(filePath); err != nil {
		log.Printf("⚠️ Failed to delete file %s: %v", filePath, err)
	} else {
		log.Printf("🗑️ Deleted file %s after sending", filePath)
	}

	return nil
}
