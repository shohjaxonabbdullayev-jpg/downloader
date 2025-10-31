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
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ytDlpPath        = "yt-dlp"
	galleryDlPath    = "gallery-dl"
	downloadsDir     = "downloads"
	instaCookiesFile = "cookies.txt"
)

func main() {
	// Load environment variables
	_ = godotenv.Load()

	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("❌ BOT_TOKEN not found in .env file")
	}

	// Ensure downloads directory exists
	os.MkdirAll(downloadsDir, 0755)

	// Start Telegram bot
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatal(err)
	}
	bot.Debug = false
	log.Printf("🤖 Authorized as @%s", bot.Self.UserName)

	// Start health check server (for Render)
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, "Bot is running")
		})
		log.Println("💚 Starting health check server on port 10000")
		http.ListenAndServe(":10000", nil)
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		msg := update.Message
		url := strings.TrimSpace(msg.Text)
		if url == "" {
			continue
		}

		// Validate URL
		if !isValidURL(url) {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❌ Please send a valid link."))
			continue
		}

		// Acknowledge
		loadingMsg := tgbotapi.NewMessage(msg.Chat.ID, "⏳ Downloading, please wait...")
		sentMsg, _ := bot.Send(loadingMsg)

		// Perform download
		files, err := downloadMedia(url)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("❌ Error: %v", err)))
			continue
		}

		if len(files) == 0 {
			bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⚠️ No files downloaded."))
			continue
		}

		// Send files to user
		for _, file := range files {
			if isImage(file) {
				sendPhoto(bot, msg.Chat.ID, file)
			} else {
				sendDocument(bot, msg.Chat.ID, file)
			}
		}

		// Delete “downloading...” message
		deleteMsg := tgbotapi.NewDeleteMessage(msg.Chat.ID, sentMsg.MessageID)
		bot.Request(deleteMsg)
	}
}

// ---------------------- Downloader Logic ----------------------

func downloadMedia(url string) ([]string, error) {
	start := time.Now()
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	// 1️⃣ YouTube / TikTok / Instagram video
	if strings.Contains(url, "youtu") || strings.Contains(url, "tiktok.com") || strings.Contains(url, "reel/") {
		log.Printf("🎥 Using yt-dlp for video download")
		args := []string{"-f", "bestvideo+bestaudio/best", "--merge-output-format", "mp4", "-o", outputTemplate, url}
		out, err := runCommandCapture(ytDlpPath, args...)
		log.Println(out)
		if err != nil {
			return nil, fmt.Errorf("yt-dlp error: %v", err)
		}
		return filesCreatedAfter(downloadsDir, start), nil
	}

	// 2️⃣ Instagram images / galleries
	if strings.Contains(url, "instagram.com") {
		log.Printf("🖼️ Using gallery-dl for image/gallery")

		args := []string{"-d", downloadsDir, url}
		if fileExists(instaCookiesFile) {
			log.Printf("🍪 Using Instagram cookies.txt file")
			args = append(args, "--cookies", instaCookiesFile)
		} else {
			log.Printf("⚠️ No cookies.txt found — Instagram download may fail")
		}

		out, err := runCommandCapture(galleryDlPath, args...)
		log.Println(out)
		if err != nil {
			return nil, fmt.Errorf("gallery-dl error: %v", err)
		}

		return filesCreatedAfter(downloadsDir, start), nil
	}

	// 3️⃣ Fallback for other sites
	log.Printf("🌐 Using gallery-dl (generic)")
	args := []string{"-d", downloadsDir, url}
	out, err := runCommandCapture(galleryDlPath, args...)
	log.Println(out)
	if err != nil {
		return nil, fmt.Errorf("gallery-dl error: %v", err)
	}

	return filesCreatedAfter(downloadsDir, start), nil
}

// ---------------------- Helper Functions ----------------------

func runCommandCapture(name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String() + "\n" + stderr.String()
	return out, err
}

func filesCreatedAfter(dir string, since time.Time) []string {
	var results []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.ModTime().After(since) {
			results = append(results, path)
		}
		return nil
	})
	return results
}

func isValidURL(text string) bool {
	re := regexp.MustCompile(`https?://[^\s]+`)
	return re.MatchString(text)
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	return err == nil && !info.IsDir()
}

func isImage(file string) bool {
	lower := strings.ToLower(file)
	return strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".webp")
}

func sendDocument(bot *tgbotapi.BotAPI, chatID int64, file string) {
	f, err := os.Open(file)
	if err != nil {
		log.Printf("❌ Failed to open document: %v", err)
		return
	}
	defer f.Close()

	doc := tgbotapi.NewDocument(chatID, tgbotapi.FileReader{
		Name:   filepath.Base(file),
		Reader: f,
	})
	if _, err := bot.Send(doc); err != nil {
		log.Printf("❌ Failed to send document: %v", err)
	}
}

func sendPhoto(bot *tgbotapi.BotAPI, chatID int64, file string) {
	f, err := os.Open(file)
	if err != nil {
		log.Printf("❌ Failed to open image: %v", err)
		return
	}
	defer f.Close()

	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileReader{
		Name:   filepath.Base(file),
		Reader: f,
	})
	if _, err := bot.Send(photo); err != nil {
		log.Printf("❌ Failed to send photo: %v", err)
	}
}
