package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
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
	ffmpegPath = "/usr/bin" // Render/Docker Linux environment
	ytDlpPath  = "yt-dlp"
)

var (
	downloadsDir         = "downloads"
	instaCookiesFile     = "cookies.txt"
	youtubeCookiesFile   = "youtube_cookies.txt"
	pinterestCookiesFile = "pinterest_cookies.txt"
	sem                  = make(chan struct{}, 3) // limit concurrent downloads
)

// ===================== HEALTH CHECK SERVER =====================
func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "✅ Bot is running and healthy!")
	})

	log.Printf("💚 Starting health check server on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Health check server failed: %v", err)
	}
}

// ===================== MAIN =====================
func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env file not found, using system environment")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set in environment or .env file")
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
		if update.Message == nil {
			continue
		}
		go handleMessage(bot, update.Message)
	}
}

// ===================== MESSAGE HANDLER =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	chatID := msg.Chat.ID

	if text == "/start" {
		startMsg := "👋 Salom!\n\n🎥 Menga YouTube, Instagram, TikTok yoki Pinterest link yuboring — men sizga videoni yoki rasmni yuboraman."
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		// Normalize YouTube Shorts
		if strings.Contains(link, "youtube.com/shorts/") {
			link = strings.Replace(link, "shorts/", "watch?v=", 1)
		}

		// Resolve Pinterest short URLs (pin.it)
		if strings.Contains(link, "pin.it") {
			link = resolveShortLink(link)
		}

		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda... iltimos kuting.")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(url string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, err := downloadMedia(url)
			<-sem

			// remove "loading..." message
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				errorMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Iltimos, linkning to‘g‘ri ekanligiga ishonch hosil qiling.")
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			if len(files) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi: fayl topilmadi."))
				return
			}

			// Choose newest file out of results
			sort.Slice(files, func(i, j int) bool {
				fi, _ := os.Stat(files[i])
				fj, _ := os.Stat(files[j])
				return fi.ModTime().After(fj.ModTime())
			})
			target := files[0]

			ext := strings.ToLower(filepath.Ext(target))
			if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".webp" {
				sendPhoto(bot, chatID, target, replyToID)
			} else {
				sendVideoWithButton(bot, chatID, target, replyToID)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// ===================== LINK EXTRACTION =====================
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
		strings.Contains(text, "tiktok.com") ||
		strings.Contains(text, "pinterest.com") ||
		strings.Contains(text, "pin.it")
}

// ===================== SHORT LINK RESOLVER =====================
func resolveShortLink(url string) string {
	if !strings.Contains(url, "pin.it") {
		return url
	}

	client := &http.Client{
		// allow redirects so resp.Request.URL gets final url
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return nil },
		Timeout:       10 * time.Second,
	}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("⚠️ Failed to resolve short link %s: %v", url, err)
		return url
	}
	defer resp.Body.Close()

	finalURL := resp.Request.URL.String()
	log.Printf("🔗 Resolved short link: %s → %s", url, finalURL)
	return finalURL
}

// ===================== DOWNLOAD FUNCTION =====================
func downloadMedia(url string) ([]string, error) {
	start := time.Now()
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
	isInstagram := strings.Contains(url, "instagram.com")
	isPinterest := strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it")

	args := []string{
		"--no-playlist",
		"--no-warnings",
		"--restrict-filenames",
		"--merge-output-format", "mp4",
		"--ffmpeg-location", ffmpegPath,
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
		"--no-check-certificates",
		"-o", outputTemplate,
	}

	// Cookies
	if isYouTube && fileExists(youtubeCookiesFile) {
		args = append(args, "--cookies", youtubeCookiesFile)
	} else if isInstagram && fileExists(instaCookiesFile) {
		args = append(args, "--cookies", instaCookiesFile)
	} else if isPinterest && fileExists(pinterestCookiesFile) {
		args = append(args, "--cookies", pinterestCookiesFile)
	}

	// Instagram Stories
	if isInstagram && (strings.Contains(url, "/stories/") || strings.Contains(url, "/s/")) {
		args = append(args, "--download-archive", "insta_stories_archive.txt")
	}

	// Format selection
	if isYouTube {
		args = append(args, "-f", "bv*[height<=720]+ba/best[height<=720]/best")
	} else {
		args = append(args, "-f", "best")
	}

	args = append(args, url)

	log.Printf("⚙️ Downloading with yt-dlp: %s", url)
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)

	// Check for "No video formats found" or similar messages in output
	noVideo := strings.Contains(strings.ToLower(out), "no video formats found") ||
		strings.Contains(strings.ToLower(out), "didn't find any videos") ||
		strings.Contains(strings.ToLower(out), "no video formats")

	// Gather files produced by yt-dlp (we used uniqueID prefix)
	globPattern := filepath.Join(downloadsDir, fmt.Sprintf("%d_*.*", uniqueID))
	files, _ := filepath.Glob(globPattern)

	// If yt-dlp returned an error OR produced no files OR it printed "No video formats found"
	// OR if it's Pinterest (prefer gallery-dl for images/boards), try gallery-dl fallback.
	if err != nil || len(files) == 0 || noVideo || isPinterest {
		log.Printf("⚠️ yt-dlp didn't produce media or Pinterest/Instagram detected — trying gallery-dl for %s", url)

		// gallery-dl: download into downloadsDir
		gArgs := []string{"-d", downloadsDir, url}
		out2, gErr := runCommandCapture("gallery-dl", gArgs...)
		log.Printf("🖼️ gallery-dl output:\n%s", out2)
		if gErr == nil {
			// pick files created after start time
			candidates := filesCreatedAfter(downloadsDir, start)
			if len(candidates) > 0 {
				log.Printf("✅ gallery-dl downloaded %d files; returning newest", len(candidates))
				return candidates, nil
			}
			// As a fallback, return any file in downloads dir (newest)
			all := listAllFiles(downloadsDir)
			if len(all) > 0 {
				return all, nil
			}
		} else {
			log.Printf("⚠️ gallery-dl error: %v", gErr)
		}
	}

	// If yt-dlp worked and produced files, return them
	if len(files) > 0 {
		return files, nil
	}

	// final attempt: find any file created after start (maybe gallery-dl created something even on error)
	candidates := filesCreatedAfter(downloadsDir, start)
	if len(candidates) > 0 {
		return candidates, nil
	}

	return nil, fmt.Errorf("no file downloaded for %s", url)
}

// ===================== HELPERS =====================

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

// listAllFiles returns all non-directory files inside dir (full paths), newest first
func listAllFiles(dir string) []string {
	var files []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return files
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	// sort newest first
	sort.Slice(files, func(i, j int) bool {
		fi, _ := os.Stat(files[i])
		fj, _ := os.Stat(files[j])
		return fi.ModTime().After(fj.ModTime())
	})
	return files
}

// filesCreatedAfter returns files in dir with ModTime after t (newest first)
func filesCreatedAfter(dir string, t time.Time) []string {
	all := listAllFiles(dir)
	var res []string
	for _, f := range all {
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		if fi.ModTime().After(t.Add(-1 * time.Second)) {
			res = append(res, f)
		}
	}
	return res
}

// ===================== SENDERS =====================
func sendVideoWithButton(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	// Try sending as video first
	video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
	video.Caption = "@downloaderin123_bot orqali yuklab olindi"
	video.ReplyToMessageID = replyToMessageID

	button := tgbotapi.NewInlineKeyboardButtonURL("➕ Guruhga qo‘shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))
	keyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(button))
	video.ReplyMarkup = keyboard

	if _, err := bot.Send(video); err != nil {
		log.Printf("❌ Failed to send video %s: %v, trying as document", filePath, err)
		sendDocument(bot, chatID, filePath, replyToMessageID)
	} else {
		if err := os.Remove(filePath); err != nil {
			log.Printf("⚠️ Failed to delete file after sending video %s: %v", filePath, err)
		}
	}
}

func sendPhoto(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
	photo.Caption = "@downloaderin123_bot orqali yuklab olindi"
	photo.ReplyToMessageID = replyToMessageID
	if _, err := bot.Send(photo); err != nil {
		log.Printf("❌ Failed to send photo %s: %v — falling back to document", filePath, err)
		sendDocument(bot, chatID, filePath, replyToMessageID)
	} else {
		if err := os.Remove(filePath); err != nil {
			log.Printf("⚠️ Failed to delete file after sending photo %s: %v", filePath, err)
		}
	}
}

func sendDocument(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
	doc.Caption = "⚠️ Fayl hajmi katta bo‘lgani uchun hujjat sifatida yuborildi."
	doc.ReplyToMessageID = replyToMessageID
	if _, err := bot.Send(doc); err != nil {
		log.Printf("❌ Failed to send document %s: %v", filePath, err)
		// If everything fails, just notify user (leave file for manual inspection)
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Faylni yuborib bo‘lmadi."))
	} else {
		if err := os.Remove(filePath); err != nil {
			log.Printf("⚠️ Failed to delete file after sending document %s: %v", filePath, err)
		}
	}
}
