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
	ffmpegPath       = "/usr/bin"
	ytDlpPath        = "yt-dlp"
	instaCookiesFile = "cookies.txt"
	maxTelegramSize  = 50 * 1024 * 1024 // 50 MB
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3)
)

// ===================== HEALTH CHECK =====================
func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "✅ Bot is running and healthy!")
	})
	log.Printf("💚 Health check server running on port %s", port)
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
		startMsg := fmt.Sprintf("👋 Salom %s!\n\n🎥 Menga YouTube, Instagram (reel/story/post) yoki Pinterest link yuboring — men sizga videoni yoki rasmni yuboraman.", msg.From.UserName)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Iltimos, YouTube, Instagram yoki Pinterest link yuboring."))
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

			// remove loading message
			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				errorMsg := tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring yoki Instagram story uchun cookies.txt kerak.")
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			for _, file := range files {
				sendMedia(bot, chatID, file, replyToID, mediaType, url)
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
		strings.Contains(text, "pinterest.com") ||
		strings.Contains(text, "pin.it")
}

// ===================== DOWNLOAD FUNCTION =====================
func downloadMedia(url string) ([]string, string, error) {
	start := time.Now()
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))
	args := []string{"--no-playlist", "--no-warnings", "--restrict-filenames", "--ffmpeg-location", ffmpegPath, "-o", outputTemplate}

	mediaType := "video"

	switch {
	case strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be"):
		args = append(args, "-f", "bestvideo[height<=720]+bestaudio/best", "--recode-video", "mp4", "--merge-output-format", "mp4", url)

	case strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am"):
		if strings.Contains(url, "/stories/") {
			if !fileExists(instaCookiesFile) {
				return nil, "", fmt.Errorf("cookies.txt required for story download")
			}
			args = append(args, "--cookies", instaCookiesFile, "--recode-video", "mp4", url)
		} else {
			if fileExists(instaCookiesFile) {
				args = append(args, "--cookies", instaCookiesFile)
			}
			args = append(args, "--recode-video", "mp4", url)
		}

	case strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it"):
		out, err := runCommandCapture(ytDlpPath, append(args, url)...)
		if err == nil && out != "" {
			files := filesCreatedAfter(downloadsDir, start)
			return files, mediaType, nil
		}
		out, err = runCommandCapture("gallery-dl", "-d", downloadsDir, url)
		if err != nil {
			return nil, "", err
		}
		files := filesCreatedAfter(downloadsDir, start)
		return files, "image", nil
	}

	log.Printf("⚙️ Downloading: %s", url)
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)
	if err != nil {
		return nil, "", err
	}

	files := filesCreatedAfter(downloadsDir, start)
	return files, mediaType, nil
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

func filesCreatedAfter(dir string, t time.Time) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var res []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fullPath := filepath.Join(dir, e.Name())
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.ModTime().After(t.Add(-1 * time.Second)) {
			res = append(res, fullPath)
		}
	}
	sort.Slice(res, func(i, j int) bool {
		fi, _ := os.Stat(res[i])
		fj, _ := os.Stat(res[j])
		return fi.ModTime().After(fj.ModTime())
	})
	return res
}

// ===================== SENDERS =====================
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyToMessageID int, mediaType, url string) {
	buttonShare := tgbotapi.NewInlineKeyboardButtonURL("📤 Do'stlar bilan ulashish", "https://t.me/share/url?url="+url)
	buttonGroup := tgbotapi.NewInlineKeyboardButtonURL("➕ Guruhga qo'shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))
	keyboard := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(buttonShare, buttonGroup))

	if mediaType == "image" {
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.Caption = "@downloader_bot orqali yuklab olindi"
		photo.ReplyToMessageID = replyToMessageID
		photo.ReplyMarkup = keyboard
		bot.Send(photo)
		return
	}

	if strings.Contains(url, "instagram.com") && strings.Contains(url, "/stories/") {
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.Caption = "@downloader_bot orqali yuklab olindi"
		video.ReplyToMessageID = replyToMessageID
		video.ReplyMarkup = keyboard
		bot.Send(video)
	} else {
		info, _ := os.Stat(filePath)
		if info.Size() > maxTelegramSize {
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Video too large for Telegram. Download link: %s", filePath))
			msg.ReplyToMessageID = replyToMessageID
			bot.Send(msg)
		} else {
			doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
			doc.Caption = "@downloader_bot orqali yuklab olindi"
			doc.ReplyToMessageID = replyToMessageID
			doc.ReplyMarkup = keyboard
			bot.Send(doc)
		}
	}

	os.Remove(filePath)
}
