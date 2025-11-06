package main

import (
	"bytes"
	"fmt"
	"io"
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
	maxVideoHeight = 720
	downloadsDir   = "downloads"
)

var (
	instagramFile = "instagram.txt"
	pinterestFile = "pinterest.txt"
	sem           = make(chan struct{}, 3) // limit concurrent downloads
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env file not found, using environment")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("❌ Failed to create downloads dir: %v", err)
	}

	go startHealthCheckServer(port)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("❌ Bot init failed: %v", err)
	}
	log.Printf("🤖 Bot authorized as @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}
	}
}

// -------------------- HEALTH CHECK --------------------
func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "OK")
	})
	log.Printf("💚 Health server on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("❌ Health server failed: %v", err)
	}
}

// -------------------- MESSAGE HANDLER --------------------
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	chatID := msg.Chat.ID

	switch text {
	case "/start":
		startMsg := fmt.Sprintf("👋 Salom %s!\n\n🎥 Menga YouTube, Instagram, TikTok yoki Pinterest link yuboring — men sizga videoni yoki rasmni yuboraman.", msg.From.FirstName)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
		return
	case "/help":
		bot.Send(tgbotapi.NewMessage(chatID, "❓ Yordam uchun @nonfindable1 ga yozing."))
		return
	}

	links := extractSupportedLinks(text)
	if len(links) == 0 {
		return
	}

	for _, link := range links {
		loadingMsg := tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda... iltimos kuting.")
		loadingMsg.ReplyToMessageID = msg.MessageID
		sent, _ := bot.Send(loadingMsg)

		go func(link string, chatID int64, replyToID, loadingMsgID int) {
			sem <- struct{}{}
			files, mediaType, err := downloadMedia(link)
			<-sem

			_, _ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: loadingMsgID,
			})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", link, err)
				errorMsg := tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Yuklab bo'lmadi: %v", err))
				errorMsg.ReplyToMessageID = replyToID
				bot.Send(errorMsg)
				return
			}

			for _, f := range files {
				sendMediaAndAttachShareButtons(bot, chatID, f, replyToID, mediaType)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// -------------------- LINK EXTRACTION --------------------
func extractSupportedLinks(text string) []string {
	regex := `(https?://[^\s]+)`
	matches := regexp.MustCompile(regex).FindAllString(text, -1)
	var links []string
	for _, m := range matches {
		m = strings.TrimRight(m, ".,;!?)")
		if isSupportedLink(m) {
			links = append(links, m)
		}
	}
	return links
}

func isSupportedLink(text string) bool {
	l := strings.ToLower(text)
	return strings.Contains(l, "youtube.com") ||
		strings.Contains(l, "youtu.be") ||
		strings.Contains(l, "instagram.com") ||
		strings.Contains(l, "instagr.am") ||
		strings.Contains(l, "pinterest.com") ||
		strings.Contains(l, "pin.it") ||
		strings.Contains(l, "tiktok.com") ||
		strings.Contains(l, "vm.tiktok.com")
}

// -------------------- DOWNLOAD MEDIA --------------------
func downloadMedia(link string) ([]string, string, error) {
	start := time.Now()
	uniqueID := time.Now().UnixNano()
	output := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	switch {
	case strings.Contains(link, "youtube.com") || strings.Contains(link, "youtu.be"):
		return downloadYouTubeRapidAPI(link)
	case strings.Contains(link, "instagram.com") || strings.Contains(link, "instagr.am"):
		return downloadInstagram(link, output, start)
	case strings.Contains(link, "pinterest.com") || strings.Contains(link, "pin.it"):
		return downloadPinterest(link, output, start)
	case strings.Contains(link, "tiktok.com") || strings.Contains(link, "vm.tiktok.com"):
		return downloadTikTok(link, output, start)
	default:
		return nil, "", fmt.Errorf("unsupported link")
	}
}

// -------------------- YOUTUBE via RapidAPI --------------------
func downloadYouTubeRapidAPI(videoURL string) ([]string, string, error) {
	apiKey := os.Getenv("RAPIDAPI_KEY")
	if apiKey == "" {
		return nil, "", fmt.Errorf("RAPIDAPI_KEY not set in .env")
	}

	baseURL := "https://youtube-info-download-api.p.rapidapi.com/ajax/download.php"
	params := url.Values{}
	params.Set("format", "mp3") // can also be mp4 if API supports
	params.Set("add_info", "0")
	params.Set("url", videoURL)
	params.Set("audio_quality", "128")
	params.Set("allow_extended_duration", "false")
	params.Set("no_merge", "false")
	params.Set("audio_language", "en")

	reqURL := fmt.Sprintf("%s?%s", baseURL, params.Encode())
	client := &http.Client{Timeout: 90 * time.Second}
	req, _ := http.NewRequest("GET", reqURL, nil)
	req.Header.Set("x-rapidapi-host", "youtube-info-download-api.p.rapidapi.com")
	req.Header.Set("x-rapidapi-key", apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	filename := fmt.Sprintf("%s/%d_youtube.mp3", downloadsDir, time.Now().Unix())
	file, err := os.Create(filename)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return nil, "", err
	}

	return []string{filename}, "audio", nil
}

// -------------------- INSTAGRAM --------------------
func downloadInstagram(link, output string, start time.Time) ([]string, string, error) {
	args := []string{"--no-warnings", "--ffmpeg-location", ffmpegPath, "-f", fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best", maxVideoHeight), "-o", output, link}
	if fileExists(instagramFile) {
		args = append(args, "--cookies", instagramFile)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("Instagram output:\n%s", out)
	files := filesCreatedAfterRecursive(downloadsDir, start)
	if len(files) == 0 || err != nil {
		return nil, "", fmt.Errorf("Instagram download failed")
	}
	mediaType := "image"
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" || ext == ".mov" {
			mediaType = "video"
			break
		}
	}
	return files, mediaType, nil
}

// -------------------- PINTEREST --------------------
func downloadPinterest(link, output string, start time.Time) ([]string, string, error) {
	args := []string{"-d", downloadsDir, link}
	if fileExists(pinterestFile) {
		args = append([]string{"--cookies", pinterestFile}, args...)
	}
	out, err := runCommandCapture("gallery-dl", args...)
	log.Printf("Pinterest output:\n%s", out)
	files := filesCreatedAfterRecursive(downloadsDir, start)
	if err != nil || len(files) == 0 {
		return nil, "", fmt.Errorf("Pinterest download failed")
	}
	return files, "image", nil
}

// -------------------- TIKTOK --------------------
func downloadTikTok(link, output string, start time.Time) ([]string, string, error) {
	args := []string{"--no-warnings", "--ffmpeg-location", ffmpegPath, "-f", fmt.Sprintf("bestvideo[height<=%d]+bestaudio/best", maxVideoHeight), "-o", output, link}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("TikTok output:\n%s", out)
	files := filesCreatedAfterRecursive(downloadsDir, start)
	if err != nil || len(files) == 0 {
		return nil, "", fmt.Errorf("TikTok download failed")
	}
	return files, "video", nil
}

// -------------------- HELPERS --------------------
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

func filesCreatedAfterRecursive(dir string, t time.Time) []string {
	var res []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(t) {
			res = append(res, path)
		}
		return nil
	})
	sort.Slice(res, func(i, j int) bool {
		fi, _ := os.Stat(res[i])
		fj, _ := os.Stat(res[j])
		return fi.ModTime().Before(fj.ModTime())
	})
	return res
}

// -------------------- SEND MEDIA --------------------
func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) error {
	var sentMsg tgbotapi.Message
	var err error
	caption := "@downloaderin123_bot orqali yuklab olindi"

	switch mediaType {
	case "video":
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.ReplyToMessageID = replyTo
		video.Caption = caption
		sentMsg, err = bot.Send(video)
	case "audio":
		audio := tgbotapi.NewAudio(chatID, tgbotapi.FilePath(filePath))
		audio.ReplyToMessageID = replyTo
		audio.Caption = caption
		sentMsg, err = bot.Send(audio)
	case "image":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.ReplyToMessageID = replyTo
		photo.Caption = caption
		sentMsg, err = bot.Send(photo)
	default:
		return fmt.Errorf("unknown media type: %s", mediaType)
	}
	if err != nil {
		return err
	}

	msgLink := fmt.Sprintf("https://t.me/%s/%d", bot.Self.UserName, sentMsg.MessageID)
	shareURL := fmt.Sprintf("https://t.me/share/url?url=%s", url.QueryEscape(msgLink))
	btnShare := tgbotapi.NewInlineKeyboardButtonURL("📤 Do'stlar bilan ulashish", shareURL)
	btnGroup := tgbotapi.NewInlineKeyboardButtonURL("👥 Guruhga qo'shish", fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(btnShare),
		tgbotapi.NewInlineKeyboardRow(btnGroup),
	)

	edit := tgbotapi.NewEditMessageReplyMarkup(chatID, sentMsg.MessageID, keyboard)
	_, _ = bot.Send(edit)

	// delete file
	_ = os.Remove(filePath)
	return nil
}
