package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http" // 👈 for health check
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ffmpegPath       = "/usr/bin"
	ytDlpPath        = "/usr/local/bin/yt-dlp"
	galleryDlPath    = "/usr/local/bin/gallery-dl"
	instaCookiesFile = "cookies.txt"
	downloadsDir     = "downloads"
)

// ================== HEALTH CHECK ==================
func startHealthServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "✅ Bot is running and healthy!")
	})
	log.Printf("💚 Health check server listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// ================== RUN COMMAND ==================
func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// ================== DOWNLOAD ==================
func downloadMedia(url string) ([]string, error) {
	isYouTube := strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
	isInstagram := strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am")
	isTikTok := strings.Contains(url, "tiktok.com")

	if !isYouTube && !isInstagram && !isTikTok {
		return nil, fmt.Errorf("❌ unsupported URL")
	}

	uniqueID := time.Now().UnixNano()
	outputDir := filepath.Join(downloadsDir, fmt.Sprintf("%d", uniqueID))
	os.MkdirAll(outputDir, 0755)

	args := []string{
		"--no-warnings",
		"--no-call-home",
		"--restrict-filenames",
		"--no-playlist",
		"-o", filepath.Join(outputDir, "%(title)s.%(ext)s"),
		url,
	}

	if isInstagram {
		log.Printf("🍪 Using Instagram cookies for %s", url)
		args = append([]string{"--cookies", instaCookiesFile}, args...)
	}

	log.Printf("⚙️ Downloading with yt-dlp: %s", url)
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)

	files, _ := filepath.Glob(filepath.Join(outputDir, "*"))
	if len(files) == 0 || err != nil {
		if isInstagram {
			log.Printf("🖼️ Falling back to gallery-dl...")
			galleryDir := filepath.Join(downloadsDir, fmt.Sprintf("%d_gallery", uniqueID))
			os.MkdirAll(galleryDir, 0755)

			galleryArgs := []string{"--cookies", instaCookiesFile, "-d", galleryDir, url}
			out2, err2 := runCommandCapture(galleryDlPath, galleryArgs...)
			log.Printf("🖼️ gallery-dl output:\n%s", out2)

			if err2 != nil {
				return nil, fmt.Errorf("gallery-dl failed: %v", err2)
			}

			imgs, _ := filepath.Glob(filepath.Join(galleryDir, "**", "*.jpg"))
			if len(imgs) == 0 {
				imgs, _ = filepath.Glob(filepath.Join(galleryDir, "**", "*.png"))
			}
			if len(imgs) == 0 {
				return nil, fmt.Errorf("no files downloaded by gallery-dl")
			}
			return imgs, nil
		}
		return nil, fmt.Errorf("yt-dlp failed: %v", err)
	}

	return files, nil
}

func sendFiles(bot *tgbotapi.BotAPI, chatID int64, files []string) {
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" {
			video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(f))
			bot.Send(video)
		} else if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
			photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(f))
			bot.Send(photo)
		}
	}
}

func main() {
	godotenv.Load()

	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("❌ BOT_TOKEN missing in .env")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// 🩺 Start health server in a separate goroutine
	go startHealthServer(port)

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("❌ Failed to create bot: %v", err)
	}

	log.Printf("🤖 Bot started as @%s", bot.Self.UserName)
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		msgText := update.Message.Text
		if msgText == "/start" {
			startText := "👋 Welcome! Send me any video or photo link from:\n" +
				"📺 YouTube\n📸 Instagram (posts, reels, stories, carousels)\n🎵 TikTok\n\n" +
				"and I'll download it for you."
			bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, startText))
			continue
		}

		if msgText == "/help" {
			helpText := "💡 Send a YouTube, Instagram, or TikTok link, and I'll download it.\n\n" +
				"For issues or requests, contact my creator: @nonfindable"
			bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, helpText))
			continue
		}

		if strings.HasPrefix(msgText, "http") {
			waitMsg := tgbotapi.NewMessage(update.Message.Chat.ID, "⏳ Downloading... please wait.")
			sent, _ := bot.Send(waitMsg)

			files, err := downloadMedia(msgText)
			if err != nil {
				errMsg := fmt.Sprintf("❌ Failed: %v", err)
				bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, errMsg))
				continue
			}

			sendFiles(bot, update.Message.Chat.ID, files)

			doneMsg := tgbotapi.NewEditMessageText(update.Message.Chat.ID, sent.MessageID, "✅ Download complete!")
			bot.Send(doneMsg)
		}
	}
}
