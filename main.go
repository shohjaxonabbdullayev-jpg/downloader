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
	ffmpegPath = "/usr/bin"
	ytDlpPath  = "yt-dlp"
)

var (
	downloadsDir         = "downloads"
	instaCookiesFile     = "cookies.txt"
	pinterestCookiesFile = "pinterest_cookies.txt"
	sem                  = make(chan struct{}, 3)
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

	// Only respond to messages containing supported links
	links := extractSupportedLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		// Resolve Pinterest short links
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

			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil || len(files) == 0 {
				errorMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Iltimos, linkning to‘g‘ri ekanligiga ishonch hosil qiling.")
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

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
	return strings.Contains(text, "instagram.com") ||
		strings.Contains(text, "instagr.am") ||
		strings.Contains(text, "tiktok.com") ||
		strings.Contains(text, "pinterest.com") ||
		strings.Contains(text, "pin.it") ||
		strings.Contains(text, "youtube.com") ||
		strings.Contains(text, "youtu.be")
}

// ===================== SHORT LINK RESOLVER =====================
func resolveShortLink(url string) string {
	if !strings.Contains(url, "pin.it") {
		return url
	}
	client := &http.Client{
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

	isInstagram := strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am")
	isPinterest := strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it")
	isTikTok := strings.Contains(url, "tiktok.com")
	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")

	// ---------------- Pinterest & Instagram ----------------
	if isPinterest || isInstagram {
		log.Printf("⚙️ Attempting gallery-dl download: %s", url)
		args := []string{"-d", downloadsDir, url}
		if isPinterest && fileExists(pinterestCookiesFile) {
			args = append(args, "--cookies", pinterestCookiesFile)
		}
		if isInstagram && fileExists(instaCookiesFile) {
			args = append(args, "--cookies", instaCookiesFile)
		}
		out, err := runCommandCapture("gallery-dl", args...)
		if err != nil {
			log.Println(out)
		}
		files := filesCreatedAfter(downloadsDir, start)
		if len(files) > 0 {
			return files, nil
		}
	}

	// ---------------- yt-dlp fallback for all video platforms ----------------
	if isPinterest || isInstagram || isTikTok || isYouTube {
		log.Printf("⚙️ Attempting yt-dlp download: %s", url)
		args := []string{"--no-playlist", "-o", outputTemplate, url}

		// Add cookies if needed
		if isPinterest && fileExists(pinterestCookiesFile) {
			args = append(args, "--cookies", pinterestCookiesFile)
		}
		if isInstagram && fileExists(instaCookiesFile) {
			args = append(args, "--cookies", instaCookiesFile)
		}

		out, err := runCommandCapture(ytDlpPath, args...)
		if err != nil {
			log.Printf("❌ Command failed: %v\nOutput: %s", err, out)
		}

		files := filesCreatedAfter(downloadsDir, start)
		if len(files) > 0 {
			return files, nil
		}
	}

	return nil, fmt.Errorf("failed to download content: %s", url)
}

// ===================== HELPERS =====================
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func runCommandCapture(name string, args ...string) (string, error) {
	log.Printf("⚙️ Running command: %s %s", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

func filesCreatedAfter(dir string, t time.Time) []string {
	var res []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return res
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.ModTime().After(t.Add(-1 * time.Second)) {
			res = append(res, filepath.Join(dir, e.Name()))
		}
	}
	return res
}

// ===================== SENDERS =====================
func sendVideoWithButton(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
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
		os.Remove(filePath)
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
		os.Remove(filePath)
	}
}

func sendDocument(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int) {
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
	doc.Caption = "⚠️ Fayl hajmi katta bo‘lgani uchun hujjat sifatida yuborildi."
	doc.ReplyToMessageID = replyToMessageID
	if _, err := bot.Send(doc); err != nil {
		log.Printf("❌ Failed to send document %s: %v", filePath, err)
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Faylni yuborib bo‘lmadi."))
	} else {
		os.Remove(filePath)
	}
}
