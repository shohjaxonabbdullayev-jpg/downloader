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
	sem          = make(chan struct{}, 3) // max 3 parallel downloads
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

	// Health check for hosting platforms (Render, Railway, etc.)
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		})
		log.Fatal(http.ListenAndServe(":"+port, nil))
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	for update := range bot.GetUpdatesChan(u) {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}
	}
}

/* ================= MESSAGE HANDLER ================= */
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(
			chatID,
			"👋 Salom!\n\nInstagram, TikTok, X, Facebook yoki Pinterest link yuboring.\nMen ENG YUQORI sifatda yuklab beraman 🚀",
		))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		return
	}

	waitMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

	go func() {
		defer bot.Request(tgbotapi.DeleteMessageConfig{
			ChatID:    chatID,
			MessageID: waitMsg.MessageID,
		})

		for _, link := range links {
			sem <- struct{}{}
			files, mediaType, err := download(link)
			<-sem

			if err != nil || len(files) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Yuklab bo‘lmadi: "+link))
				continue
			}

			caption := "⬇️ @downloaderin123_bot"

			// Multiple files → send as album (MediaGroup)
			if len(files) > 1 {
				var media []interface{}
				for i, f := range files {
					if mediaType == "video" {
						input := tgbotapi.NewInputMediaVideo(tgbotapi.FilePath(f))
						input.SupportsStreaming = true
						if i == 0 {
							input.Caption = caption
						}
						media = append(media, input)
					} else {
						input := tgbotapi.NewInputMediaPhoto(tgbotapi.FilePath(f))
						if i == 0 {
							input.Caption = caption
						}
						media = append(media, input)
					}
				}

				album := tgbotapi.NewMediaGroup(chatID, media)
				album.ReplyToMessageID = msg.MessageID
				bot.Send(album)
			} else {
				// Single file
				if mediaType == "video" {
					v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(files[0]))
					v.Caption = caption
					v.ReplyToMessageID = msg.MessageID
					v.SupportsStreaming = true
					bot.Send(v)
				} else {
					p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(files[0]))
					p.Caption = caption
					p.ReplyToMessageID = msg.MessageID
					bot.Send(p)
				}
			}

			// Cleanup files
			for _, f := range files {
				_ = os.Remove(f)
			}
		}
	}()
}

/* ================= LINK PARSER ================= */
func extractLinks(text string) []string {
	re := regexp.MustCompile(`https?://\S+`)
	raw := re.FindAllString(text, -1)

	var links []string
	for _, u := range raw {
		if isSupported(u) {
			links = append(links, u)
		}
	}
	return links
}

func isSupported(u string) bool {
	u = strings.ToLower(u)
	return strings.Contains(u, "instagram") ||
		strings.Contains(u, "tiktok") ||
		strings.Contains(u, "twitter.com") ||
		strings.Contains(u, "x.com") ||
		strings.Contains(u, "facebook") ||
		strings.Contains(u, "fb.watch") ||
		strings.Contains(u, "pinterest") ||
		strings.Contains(u, "pin.it")
}

/* ================= DOWNLOAD ================= */
func download(link string) ([]string, string, error) {
	start := time.Now()

	// Unique output template
	out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title).100s_%%(id)s", time.Now().UnixNano()))

	args := []string{
		"--no-warnings",
		"--yes-playlist",
		// Highest quality, but compatible MP4 (H.264 + AAC)
		"-f", "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best",
		"--merge-output-format", "mp4",
		"--postprocessor-args", "ffmpeg:-movflags +faststart",
		"-o", out + ".%(ext)s",
		link,
	}

	applyCookies(&args, link)

	_, errRun := run(ytDlpPath, args...)
	if errRun != nil {
		log.Printf("yt-dlp error: %v", errRun)
	}

	files := recentFiles(start)
	if len(files) > 0 {
		return files, detectType(files), nil
	}

	// Fallback for images/carousels
	_, _ = run(galleryDlPath, "-d", downloadsDir, link)
	files = recentFiles(start)
	if len(files) > 0 {
		return files, "image", nil
	}

	return nil, "", fmt.Errorf("download failed for %s", link)
}

/* ================= EXEC ================= */
func run(cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	return buf.String(), c.Run()
}

/* ================= FILE UTILS ================= */
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
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" || ext == ".mkv" {
			return "video"
		}
	}
	return "image"
}

/* ================= COOKIES ================= */
func applyCookies(args *[]string, link string) {
	add := func(domain, file string) {
		if strings.Contains(link, domain) && fileExists(file) {
			*args = append([]string{"--cookies", file}, *args...)
		}
	}
	add("instagram.com", "instagram.txt")
	add("twitter.com", "twitter.txt")
	add("x.com", "twitter.txt")
	add("facebook.com", "facebook.txt")
	add("pinterest.com", "pinterest.txt")
}

func fileExists(p string) bool {
	i, err := os.Stat(p)
	return err == nil && !i.IsDir()
}
