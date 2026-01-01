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

/* ================= CONFIG ================= */
const (
	ytDlpPath     = "yt-dlp"
	galleryDlPath = "gallery-dl"
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 2) // safer on Render
)

/* ================= MAIN ================= */
func main() {
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("BOT_TOKEN missing")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	_ = os.MkdirAll(downloadsDir, 0755)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Bot started: @%s", bot.Self.UserName)

	// 🔒 IMPORTANT: delete webhook to avoid conflict
	_, _ = bot.Request(tgbotapi.DeleteWebhookConfig{})

	// Health check (Render requirement)
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		})
		http.ListenAndServe(":"+port, nil)
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			handleMessage(bot, update.Message)
		}
	}
}

/* ================= MESSAGE HANDLER ================= */
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	chatID := msg.Chat.ID

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(chatID,
			"👋 Salom!\nInstagram, TikTok, X, Facebook yoki Pinterest link yuboring.",
		))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		return
	}

	wait, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

	for _, link := range links {
		sem <- struct{}{}
		files, mediaType := safeDownload(link)
		<-sem

		if len(files) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Yuklab bo‘lmadi:\n"+link))
			continue
		}

		caption := "⬇️ @downloaderin123_bot"

		if len(files) > 1 {
			var media []interface{}
			for i, f := range files {
				if mediaType == "video" {
					m := tgbotapi.NewInputMediaVideo(tgbotapi.FilePath(f))
					m.SupportsStreaming = true
					if i == 0 {
						m.Caption = caption
					}
					media = append(media, m)
				} else {
					m := tgbotapi.NewInputMediaPhoto(tgbotapi.FilePath(f))
					if i == 0 {
						m.Caption = caption
					}
					media = append(media, m)
				}
			}
			bot.Send(tgbotapi.NewMediaGroup(chatID, media))
		} else {
			if mediaType == "video" {
				v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(files[0]))
				v.SupportsStreaming = true
				v.Caption = caption
				bot.Send(v)
			} else {
				p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(files[0]))
				p.Caption = caption
				bot.Send(p)
			}
		}

		for _, f := range files {
			_ = os.Remove(f)
		}
	}

	_ = bot.Request(tgbotapi.DeleteMessageConfig{
		ChatID:    chatID,
		MessageID: wait.MessageID,
	})
}

/* ================= DOWNLOAD SAFE ================= */
func safeDownload(link string) ([]string, string) {
	start := time.Now()

	out := filepath.Join(
		downloadsDir,
		fmt.Sprintf("%d_%%(id)s", time.Now().UnixNano()),
	)

	args := []string{
		"-f", "best[ext=mp4]/best",
		"--merge-output-format", "mp4",
		"--postprocessor-args", "ffmpeg:-movflags +faststart -pix_fmt yuv420p",
		"-o", out + ".%(ext)s",
		link,
	}

	_, err := run(ytDlpPath, args...)
	if err != nil {
		log.Println("yt-dlp failed:", err)
	}

	files := recentFiles(start)
	if len(files) > 0 {
		return files, detectType(files)
	}

	// image fallback
	_, _ = run(galleryDlPath, "-d", downloadsDir, link)
	files = recentFiles(start)
	return files, "image"
}

/* ================= UTILS ================= */
func run(cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	return buf.String(), c.Run()
}

func extractLinks(text string) []string {
	re := regexp.MustCompile(`https?://\S+`)
	all := re.FindAllString(text, -1)

	var out []string
	for _, u := range all {
		if strings.Contains(u, "instagram") ||
			strings.Contains(u, "tiktok") ||
			strings.Contains(u, "facebook") ||
			strings.Contains(u, "twitter") ||
			strings.Contains(u, "x.com") ||
			strings.Contains(u, "pinterest") {
			out = append(out, u)
		}
	}
	return out
}

func recentFiles(since time.Time) []string {
	var files []string
	filepath.Walk(downloadsDir, func(path string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && info.ModTime().After(since) {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func detectType(files []string) string {
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f), ".mp4") {
			return "video"
		}
	}
	return "image"
}
