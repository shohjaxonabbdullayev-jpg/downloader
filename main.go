// FULL REWRITTEN CODE WITH TIKTOK + FACEBOOK + TWITTER SUPPORT
// -------------------------------------------------------------

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
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ffmpegPath     = "/usr/bin/ffmpeg"
	ytDlpPath      = "yt-dlp"
	maxVideoHeight = 480
)

var (
	downloadsDir  = "downloads"
	instagramFile = "instagram.txt"
	youtubeFile   = "youtube.txt"
	pinterestFile = "pinterest.txt"
	tiktokFile    = "tiktok.txt"
	facebookFile  = "facebook.txt"
	twitterFile   = "twitter.txt"
	sem           = make(chan struct{}, 3)
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env not found")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN missing")
	}

	go startHealthCheckServer("8080")

	os.MkdirAll(downloadsDir, 0755)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("🤖 Bot Started:", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}
	}
}

func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "OK")
	})
	http.ListenAndServe(":"+port, nil)
}

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		loading, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ Yuklanmoqda..."))

		go func(url string, chatID int64, replyID, loadingID int) {
			sem <- struct{}{}
			files, mediaType, err := downloadMedia(url)
			<-sem

			bot.Request(tgbotapi.NewDeleteMessage(chatID, loadingID))

			if err != nil {
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi"))
				return
			}

			for _, file := range files {
				sendMediaAndAttachShareButtons(bot, chatID, file, replyID, mediaType)
			}
		}(link, msg.Chat.ID, msg.MessageID, loading.MessageID)
	}
}

func extractSupportedLinks(text string) []string {
	regex := `(https?://[^\s]+)`
	links := regexp.MustCompile(regex).FindAllString(text, -1)
	var out []string
	for _, l := range links {
		lc := strings.ToLower(l)
		if strings.Contains(lc, "youtube") ||
			strings.Contains(lc, "youtu.be") ||
			strings.Contains(lc, "instagram") ||
			strings.Contains(lc, "instagr") ||
			strings.Contains(lc, "pinterest") ||
			strings.Contains(lc, "pin.it") ||
			strings.Contains(lc, "tiktok.com") ||
			strings.Contains(lc, "facebook.com") ||
			strings.Contains(lc, "fb.watch") ||
			strings.Contains(lc, "twitter.com") ||
			strings.Contains(lc, "x.com") {
			out = append(out, l)
		}
	}
	return out
}

func downloadMedia(url string) ([]string, string, error) {
	start := time.Now()
	id := time.Now().UnixNano()
	output := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", id))

	switch {
	case strings.Contains(url, "youtube") || strings.Contains(url, "youtu.be"):
		return downloadUsingYtDlp(url, output, youtubeFile, start)
	case strings.Contains(url, "instagram"):
		return downloadUsingYtDlp(url, output, instagramFile, start)
	case strings.Contains(url, "tiktok"):
		return downloadUsingYtDlp(url, output, tiktokFile, start)
	case strings.Contains(url, "facebook") || strings.Contains(url, "fb.watch"):
		return downloadWithFallback(url, output, facebookFile, start)
	case strings.Contains(url, "twitter") || strings.Contains(url, "x.com"):
		return downloadWithFallback(url, output, twitterFile, start)
	case strings.Contains(url, "pinterest") || strings.Contains(url, "pin.it"):
		return downloadWithFallback(url, output, pinterestFile, start)
	}

	return nil, "", fmt.Errorf("unsupported")
}

func downloadUsingYtDlp(url, output, cookie string, start time.Time) ([]string, string, error) {
	args := []string{
		"--no-warnings",
		"--ffmpeg-location", ffmpegPath,
		"-f", fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best", maxVideoHeight),
		"--merge-output-format", "mp4",
		"-o", output,
		url,
	}
	if fileExists(cookie) {
		args = append(args, "--cookies", cookie)
	}

	out, err := runCommandCapture(ytDlpPath, args...)
	log.Println(out)

	files := filesCreatedAfterRecursive(downloadsDir, start)
	return files, "video", err
}

func downloadWithFallback(url, output, cookie string, start time.Time) ([]string, string, error) {
	files, media, err := downloadUsingYtDlp(url, output, cookie, start)
	if err == nil && len(files) > 0 {
		return files, media, nil
	}

	args := []string{"-d", downloadsDir, url}
	if fileExists(cookie) {
		args = []string{"--cookies", cookie, "-d", downloadsDir, url}
	}
	runCommandCapture("gallery-dl", args...)

	files = filesCreatedAfterRecursive(downloadsDir, start)
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no media")
	}

	mediaType := "image"
	for _, f := range files {
		if strings.HasSuffix(f, ".mp4") {
			mediaType = "video"
			break
		}
	}

	return files, mediaType, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
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
	var out []string
	filepath.Walk(dir, func(path string, info os.FileInfo, _ error) error {
		if !info.IsDir() && info.ModTime().After(t) {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, file string, reply int, mediaType string) {
	cap := "@downloaderin123_bot orqali yuklab olindi"

	var msg tgbotapi.Message
	var err error

	if mediaType == "video" {
		m := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(file))
		m.Caption = cap
		m.ReplyToMessageID = reply
		msg, err = bot.Send(m)
	} else {
		m := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(file))
		m.Caption = cap
		m.ReplyToMessageID = reply
		msg, err = bot.Send(m)
	}

	if err == nil {
		link := fmt.Sprintf("https://t.me/%s/%d", bot.Self.UserName, msg.MessageID)
		share := fmt.Sprintf("https://t.me/share/url?url=%s", url.QueryEscape(link))
		keyboard := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonURL("📤 Ulashish", share)),
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonURL("👥 Guruhga qo'shish", "https://t.me/"+bot.Self.UserName+"?startgroup=true")),
		)
		bot.Send(tgbotapi.NewEditMessageReplyMarkup(chatID, msg.MessageID, keyboard))
	}

	os.Remove(file)
}
