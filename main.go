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
	sem           = make(chan struct{}, 3)
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env file not found")
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
		log.Fatalf("❌ Cannot create downloads folder: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Bot init failed: %v", err)
	}

	log.Printf("🤖 Bot launched as @%s", bot.Self.UserName)

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
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "OK")
	})
	log.Printf("💚 Health check running on %s", port)
	http.ListenAndServe(":"+port, nil)
}

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	switch text {
	case "/start":
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID,
			fmt.Sprintf("👋 Salom %s!\n\n🎥 Link yuboring — video/rasm yuklab beraman.", msg.From.FirstName)))
		return
	case "/help":
		bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "❓ Yordam: @nonfindable1"))
		return
	}

	links := extractSupportedLinks(text)
	for _, link := range links {
		loading, _ := bot.Send(tgbotapi.NewMessage(msg.Chat.ID, "⏳ Yuklanmoqda..."))
		go downloadAndSend(bot, link, msg.Chat.ID, msg.MessageID, loading.MessageID)
	}
}

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
		strings.Contains(text, "pin.it") ||
		strings.Contains(text, "tiktok.com") ||
		strings.Contains(text, "vm.tiktok.com")
}

func downloadAndSend(bot *tgbotapi.BotAPI, url string, chatID int64, replyToID, loadingID int) {
	sem <- struct{}{}
	files, mediaType, err := downloadMedia(url)
	<-sem

	bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: loadingID})

	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi"))
		return
	}

	for _, file := range files {
		sendMediaAndAttachShareButtons(bot, chatID, file, replyToID, mediaType)
	}
}

func downloadMedia(url string) ([]string, string, error) {
	start := time.Now()
	uniqueID := time.Now().UnixNano()
	output := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	switch {
	case strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be"):
		return downloadYT(url, output, start)
	case strings.Contains(url, "instagram.com"):
		return downloadIG(url, output, start)
	case strings.Contains(url, "pinterest.com") || strings.Contains(url, "pin.it"):
		return downloadPinterest(url, output, start)
	case strings.Contains(url, "tiktok.com") || strings.Contains(url, "vm.tiktok.com"):
		return downloadTikTok(url, output, start)
	}
	return nil, "", fmt.Errorf("unknown link")
}

func downloadYT(url, output string, start time.Time) ([]string, string, error) {
	args := []string{
		"--no-warnings", "--no-playlist", "--restrict-filenames",
		"--ffmpeg-location", ffmpegPath,
		"-f", fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best", maxVideoHeight),
		"--merge-output-format", "mp4",
		"-o", output, url,
	}
	if fileExists(youtubeFile) {
		args = append(args, "--cookies", youtubeFile)
	}
	_, err := run(ytDlpPath, args...)
	return collect(start), "video", err
}

func downloadIG(url, output string, start time.Time) ([]string, string, error) {
	args := []string{
		"--no-warnings", "--ffmpeg-location", ffmpegPath,
		"-o", output, url,
	}
	if fileExists(instagramFile) {
		args = append(args, "--cookies", instagramFile)
	}
	_, err := run(ytDlpPath, args...)
	files := collect(start)
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no files")
	}
	mediaType := "image"
	for _, f := range files {
		if strings.HasSuffix(f, ".mp4") {
			mediaType = "video"
		}
	}
	return files, mediaType, err
}

func downloadPinterest(url, output string, start time.Time) ([]string, string, error) {
	args := []string{"--no-warnings", "--ffmpeg-location", ffmpegPath, "-o", output, url}
	if fileExists(pinterestFile) {
		args = append(args, "--cookies", pinterestFile)
	}
	run(ytDlpPath, args...)
	files := collect(start)
	if len(files) > 0 {
		return files, "video", nil
	}
	argsGD := []string{"-d", downloadsDir, url}
	if fileExists(pinterestFile) {
		argsGD = []string{"--cookies", pinterestFile, "-d", downloadsDir, url}
	}
	run("gallery-dl", argsGD...)
	return collect(start), "image", nil
}

func downloadTikTok(url, output string, start time.Time) ([]string, string, error) {
	args := []string{
		"--no-warnings", "--restrict-filenames",
		"--ffmpeg-location", ffmpegPath,
		"-f", fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best", maxVideoHeight),
		"--merge-output-format", "mp4",
		"-o", output, url,
	}
	_, err := run(ytDlpPath, args...)
	return collect(start), "video", err
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	return b.String(), cmd.Run()
}

func collect(t time.Time) []string {
	var out []string
	filepath.Walk(downloadsDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.ModTime().After(t) {
			out = append(out, path)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		a, _ := os.Stat(out[i])
		b, _ := os.Stat(out[j])
		return a.ModTime().Before(b.ModTime())
	})
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) error {
	var sent tgbotapi.Message
	var err error
	caption := "@downloaderin123_bot orqali yuklab olindi"

	if mediaType == "video" {
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.Caption = caption
		video.ReplyToMessageID = replyTo
		sent, err = bot.Send(video)
	} else {
		img := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		img.Caption = caption
		img.ReplyToMessageID = replyTo
		sent, err = bot.Send(img)
	}
	if err != nil {
		return err
	}

	msgLink := fmt.Sprintf("https://t.me/%s/%d", bot.Self.UserName, sent.MessageID)
	share := tgbotapi.NewInlineKeyboardButtonURL("📤 Ulashtirish", "https://t.me/share/url?url="+url.QueryEscape(msgLink))
	group := tgbotapi.NewInlineKeyboardButtonURL("👥 Guruhga qo‘shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(share),
		tgbotapi.NewInlineKeyboardRow(group),
	)
	bot.Send(tgbotapi.NewEditMessageReplyMarkup(chatID, sent.MessageID, keyboard))

	os.Remove(filePath)
	return nil
}
