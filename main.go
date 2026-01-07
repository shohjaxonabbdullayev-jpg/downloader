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
	ytDlpPath      = "yt-dlp"
	galleryDlPath  = "gallery-dl"
	maxParallel    = 3
	maxVideoHeight = "1080"
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, maxParallel)
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

	// Health check
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
			"👋 Salom!\n\nInstagram, TikTok, YouTube, X, Facebook yoki Pinterest link yuboring.\nVideo va rasmlarni **eng mos va ochiladigan formatda** yuklab beraman 🚀",
		))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "❌ Link topilmadi"))
		return
	}

	waitMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))
	defer bot.Request(tgbotapi.DeleteMessageConfig{
		ChatID:    chatID,
		MessageID: waitMsg.MessageID,
	})

	for _, link := range links {
		sem <- struct{}{}
		files, mediaType, err := download(link)
		<-sem

		if err != nil || len(files) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Yuklab bo‘lmadi: %s", link)))
			continue
		}

		for _, f := range files {
			sendMedia(bot, chatID, f, msg.MessageID, mediaType)
			_ = os.Remove(f)
		}
	}
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
		strings.Contains(u, "pin.it") ||
		strings.Contains(u, "youtube.com") ||
		strings.Contains(u, "youtu.be")
}

/* ================= DOWNLOAD ================= */

func download(link string) ([]string, string, error) {
	start := time.Now()

	out := filepath.Join(
		downloadsDir,
		fmt.Sprintf("%d_%%(title).80s_%%(id)s.%%(ext)s", time.Now().Unix()),
	)

	args := []string{
		"--no-warnings",
		"--yes-playlist",
		"-f", fmt.Sprintf("bv*[vcodec^=avc1][height<=%s]+ba[acodec^=mp4a]/b[ext=mp4]/b", maxVideoHeight),
		"--merge-output-format", "mp4",
		"--postprocessor-args", "ffmpeg:-movflags +faststart -pix_fmt yuv420p",
		"-o", out,
		link,
	}

	// Apply cookies: YouTube auto from browser, others from files
	applyCookies(&args, link)

	_, _ = run(ytDlpPath, args...)
	files := recentFiles(start)
	if len(files) > 0 {
		return files, detectType(files), nil
	}

	// Fallback: gallery-dl for images
	_, _ = run(galleryDlPath, "-d", downloadsDir, link)
	files = recentFiles(start)
	if len(files) > 0 {
		return files, "image", nil
	}

	return nil, "", fmt.Errorf("download failed")
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
	_ = filepath.Walk(downloadsDir, func(path string, info os.FileInfo, _ error) error {
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
		if ext == ".mp4" || ext == ".mov" {
			return "video"
		}
	}
	return "image"
}

/* ================= SENDER ================= */

func sendMedia(bot *tgbotapi.BotAPI, chatID int64, file string, replyTo int, mediaType string) {
	caption := "⬇️ @downloaderin123_bot"

	if mediaType == "video" {
		v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(file))
		v.Caption = caption
		v.SupportsStreaming = true
		v.ReplyToMessageID = replyTo
		bot.Send(v)
	} else {
		p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(file))
		p.Caption = caption
		p.ReplyToMessageID = replyTo
		bot.Send(p)
	}
}

/* ================= COOKIES ================= */

func applyCookies(args *[]string, link string) {
	add := func(domain, file string) {
		if strings.Contains(link, domain) && fileExists(file) {
			*args = append([]string{"--cookies", file}, *args...)
		}
	}

	switch {
	case strings.Contains(link, "youtube.com") || strings.Contains(link, "youtu.be"):
		// Automatically fetch cookies from browser (Chrome)
		*args = append([]string{"--cookies-from-browser", "chrome"}, *args...)
	default:
		add("instagram", "instagram.txt")
		add("twitter", "twitter.txt")
		add("facebook", "facebook.txt")
		add("pinterest", "pinterest.txt")
	}
}

func fileExists(p string) bool {
	i, err := os.Stat(p)
	return err == nil && !i.IsDir()
}
