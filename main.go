package main

import (
	"bytes"
	"context"
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

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ffmpegPath  = "/usr/bin/ffmpeg"
	ytDlpPath   = "yt-dlp"
	cookiesFile = "cookies.txt"
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3)
)

func main() {
	// Load environment
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env not found, using system environment")
	}
	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN not set in .env")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	go startHealthCheckServer(port)

	// Create directories
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("❌ Failed to create downloads folder: %v", err)
	}

	// Ensure cookies exist
	if !fileExists(cookiesFile) {
		log.Println("🍪 No cookies found, fetching new ones...")
		if err := fetchCookies("https://www.instagram.com/accounts/login/"); err != nil {
			log.Fatalf("❌ Failed to get cookies: %v", err)
		}
	}

	// Telegram bot setup
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

// ===================== HEALTH CHECK =====================
func startHealthCheckServer(port string) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "✅ Bot running fine!")
	})
	log.Printf("💚 Health server on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// ===================== MESSAGE HANDLER =====================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}
	chatID := msg.Chat.ID

	if text == "/start" {
		startMsg := fmt.Sprintf(
			"👋 Salom %s!\n\n🎥 Menga YouTube, Instagram yoki Pinterest link yuboring — men sizga videoni yoki rasmni yuboraman.",
			msg.From.FirstName,
		)
		bot.Send(tgbotapi.NewMessage(chatID, startMsg))
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

		go func(url string, chatID int64, replyTo, loadingMsgID int) {
			sem <- struct{}{}
			files, mediaType, err := downloadMedia(url)
			<-sem

			bot.Request(tgbotapi.DeleteMessageConfig{ChatID: chatID, MessageID: loadingMsgID})

			if err != nil {
				log.Printf("❌ Download error for %s: %v", url, err)
				bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("⚠️ Yuklab bo‘lmadi: %v", err)))
				return
			}

			for _, f := range files {
				sendMediaAndAttachShareButtons(bot, chatID, f, replyTo, mediaType)
			}
		}(link, chatID, msg.MessageID, sent.MessageID)
	}
}

// ===================== LINK EXTRACTION =====================
func extractSupportedLinks(text string) []string {
	reg := `(https?://[^\s]+)`
	matches := regexp.MustCompile(reg).FindAllString(text, -1)
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

// ===================== DOWNLOAD LOGIC =====================
func downloadMedia(url string) ([]string, string, error) {
	start := time.Now()
	unique := time.Now().UnixNano()
	output := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", unique))

	switch {
	case strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be"):
		return downloadGeneric(url, output, start, "video")
	case strings.Contains(url, "instagram.com"), strings.Contains(url, "instagr.am"):
		return downloadGeneric(url, output, start, "auto")
	case strings.Contains(url, "pinterest.com"), strings.Contains(url, "pin.it"):
		return downloadPinterest(url, output, start)
	}
	return nil, "", fmt.Errorf("unsupported link")
}

func downloadGeneric(url, output string, start time.Time, mediaHint string) ([]string, string, error) {
	args := []string{
		"--no-warnings",
		"--ffmpeg-location", ffmpegPath,
		"-o", output,
		url,
	}
	if fileExists(cookiesFile) {
		args = append(args, "--cookies", cookiesFile)
	}
	out, err := runCommandCapture(ytDlpPath, args...)
	log.Printf("🧾 yt-dlp output:\n%s", out)
	files := filesCreatedAfterRecursive(downloadsDir, start)

	if len(files) == 0 {
		return nil, "", fmt.Errorf("no media downloaded")
	}

	mediaType := "image"
	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f))
		if ext == ".mp4" {
			mediaType = "video"
			break
		}
	}
	if mediaHint == "video" {
		mediaType = "video"
	}
	return files, mediaType, err
}

func downloadPinterest(url, output string, start time.Time) ([]string, string, error) {
	files, t, err := downloadGeneric(url, output, start, "auto")
	if err == nil && len(files) > 0 {
		return files, t, nil
	}
	argsGD := []string{"-d", downloadsDir, url}
	if fileExists(cookiesFile) {
		argsGD = append([]string{"--cookies", cookiesFile}, argsGD...)
	}
	out, err := runCommandCapture("gallery-dl", argsGD...)
	log.Printf("🖼️ gallery-dl output:\n%s", out)
	files = filesCreatedAfterRecursive(downloadsDir, start)
	if len(files) == 0 {
		return nil, "", fmt.Errorf("Pinterest download failed: %v", err)
	}
	return files, "image", nil
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

func filesCreatedAfterRecursive(dir string, t time.Time) []string {
	var res []string
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, _ := d.Info()
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

// ===================== MEDIA SENDER =====================
func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) error {
	var sent tgbotapi.Message
	var err error
	caption := "@downloaderin123_bot orqali yuklab olindi"

	switch mediaType {
	case "video":
		msg := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		msg.Caption = caption
		msg.ReplyToMessageID = replyTo
		sent, err = bot.Send(msg)
	case "image":
		msg := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		msg.Caption = caption
		msg.ReplyToMessageID = replyTo
		sent, err = bot.Send(msg)
	}
	if err != nil {
		return fmt.Errorf("send error: %v", err)
	}

	msgLink := fmt.Sprintf("https://t.me/%s/%d", bot.Self.UserName, sent.MessageID)
	shareURL := fmt.Sprintf("https://t.me/share/url?url=%s", url.QueryEscape(msgLink))
	btnShare := tgbotapi.NewInlineKeyboardButtonURL("📤 Do'stlar bilan ulashish", shareURL)
	btnGroup := tgbotapi.NewInlineKeyboardButtonURL("👥 Guruhga qo'shish",
		fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName))
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(btnShare),
		tgbotapi.NewInlineKeyboardRow(btnGroup),
	)
	edit := tgbotapi.NewEditMessageReplyMarkup(chatID, sent.MessageID, keyboard)
	bot.Send(edit)
	return nil
}

// ===================== COOKIE FETCHER =====================
func fetchCookies(loginURL string) error {
	log.Println("🌐 Launching Chrome to get cookies...")

	// Configure Chrome launch options
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false), // set to true to hide Chrome
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
	)

	// Create Chrome context
	ctx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel = chromedp.NewContext(ctx)
	defer cancel()

	// Visit login page and wait for user actions
	if err := chromedp.Run(ctx,
		chromedp.Navigate(loginURL),
		chromedp.Sleep(25*time.Second), // ⏳ wait for manual login or redirects
	); err != nil {
		return fmt.Errorf("navigate failed: %v", err)
	}

	// ✅ Fetch cookies using Chrome DevTools Protocol (CDP)
	cookies, err := network.GetCookies().Do(ctx)
	if err != nil {
		return fmt.Errorf("cannot fetch cookies: %v", err)
	}

	// Save to file
	f, err := os.Create(cookiesFile)
	if err != nil {
		return fmt.Errorf("create cookies.txt failed: %v", err)
	}
	defer f.Close()

	_, _ = fmt.Fprintln(f, "# Netscape HTTP Cookie File")
	for _, c := range cookies {
		fmt.Fprintf(f, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			c.Domain,
			boolToStr(!c.HTTPOnly, "FALSE", "TRUE"),
			c.Path,
			boolToStr(c.Secure, "TRUE", "FALSE"),
			int64(c.Expires),
			c.Name,
			c.Value,
		)
	}

	log.Printf("🍪 %d cookies saved to %s", len(cookies), cookiesFile)
	return nil
}

func boolToStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
