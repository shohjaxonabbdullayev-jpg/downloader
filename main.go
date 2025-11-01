package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ytDlpPath    = "yt-dlp"
	galleryDl    = "gallery-dl"
	downloadsDir = "downloads"
	cookiesFile  = "cookies.txt"
)

func main() {
	// Load .env
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ .env not found, using environment variables")
	}

	botToken := os.Getenv("BOT_TOKEN")
	if botToken == "" {
		log.Fatal("❌ BOT_TOKEN not set in .env")
	}

	// Ensure downloads dir
	if err := os.MkdirAll(downloadsDir, 0755); err != nil {
		log.Fatalf("❌ Cannot create downloads directory: %v", err)
	}

	// Start Telegram bot
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("❌ Telegram bot init error: %v", err)
	}
	log.Printf("🤖 Authorized as @%s", bot.Self.UserName)

	// Health check
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		})
		log.Println("💚 Starting health check server on port 10000")
		http.ListenAndServe(":10000", nil)
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil || !update.Message.IsCommand() && update.Message.Text == "" {
			continue
		}
		go handleUpdate(bot, update)
	}
}

func handleUpdate(bot *tgbotapi.BotAPI, update tgbotapi.Update) {
	chatID := update.Message.Chat.ID
	url := strings.TrimSpace(update.Message.Text)

	if url == "" {
		bot.Send(tgbotapi.NewMessage(chatID, "❗ Iltimos, video yoki rasm havolasini yuboring."))
		return
	}

	bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklab olinmoqda, biroz kuting..."))

	files, err := downloadMedia(url)
	if err != nil {
		log.Printf("❌ Download error for %s: %v", url, err)
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Yuklab olishda xatolik: %v", err)))
		return
	}

	for _, filePath := range files {
		mediaType := detectMediaType(filePath)
		if err := sendMediaAndAttachShareButtons(bot, chatID, filePath, mediaType); err != nil {
			log.Printf("❌ Send error: %v", err)
		}
		os.Remove(filePath)
	}
}

// 🧩 Detect media type by file extension
func detectMediaType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".jpg" || ext == ".jpeg" || ext == ".png" {
		return "photo"
	}
	return "video"
}

// 📤 Send media + share buttons
func sendMediaAndAttachShareButtons(bot *tgbotapi.BotAPI, chatID int64, filePath string, mediaType string) error {
	caption := "@downloaderin123_bot orqali yuklab olindi"

	// Inline keyboard (share + add to group)
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("🔗 Do‘stlarga ulashish", "https://t.me/downloaderin123_bot"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL(
				"👥 Guruhga qo'shish",
				fmt.Sprintf("https://t.me/%s?startgroup=true", bot.Self.UserName),
			),
		),
	)

	var sentMsg tgbotapi.Message
	var err error

	switch strings.ToLower(mediaType) {
	case "image", "photo", ".jpg", ".jpeg", ".png":
		photo := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		photo.Caption = caption
		photo.ReplyMarkup = keyboard
		sentMsg, err = bot.Send(photo)

	case "video", ".mp4", ".mov":
		video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		video.Caption = caption
		video.SupportsStreaming = true
		video.ReplyMarkup = keyboard
		sentMsg, err = bot.Send(video)

	default:
		doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
		doc.Caption = caption
		doc.ReplyMarkup = keyboard
		sentMsg, err = bot.Send(doc)
	}

	if err != nil {
		return fmt.Errorf("media send failed: %v", err)
	}

	log.Printf("✅ Sent media successfully (MessageID: %d)", sentMsg.MessageID)
	return nil
}

// 🧠 Main download logic
func downloadMedia(url string) ([]string, error) {
	if err := ensureCookies(); err != nil {
		return nil, err
	}

	start := time.Now()
	uniqueID := time.Now().UnixNano()
	outputTemplate := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", uniqueID))

	var cmd *exec.Cmd
	isInstagram := strings.Contains(url, "instagram.com")
	isTikTok := strings.Contains(url, "tiktok.com")

	switch {
	case isInstagram:
		cmd = exec.Command(ytDlpPath, "--cookies", cookiesFile, "-o", outputTemplate, url)
	case isTikTok:
		cmd = exec.Command(ytDlpPath, "-o", outputTemplate, url)
	default:
		cmd = exec.Command(galleryDl, "-d", downloadsDir, url)
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("no media downloaded: %v\n%s", err, out.String())
	}

	log.Printf("🧾 yt-dlp output:\n%s", out.String())

	matches, _ := filepath.Glob(filepath.Join(downloadsDir, fmt.Sprintf("%d_*", uniqueID)))
	if len(matches) == 0 {
		return nil, fmt.Errorf("no media downloaded")
	}

	log.Printf("✅ Downloaded %d file(s) in %v", len(matches), time.Since(start))
	return matches, nil
}

// ✅ Ensure cookies exist
func ensureCookies() error {
	if _, err := os.Stat(cookiesFile); err == nil {
		log.Println("🍪 Using existing cookies.txt")
		return nil
	}

	log.Println("🔐 No cookies found — fetching new Instagram cookies...")
	if err := fetchCookies("https://www.instagram.com/accounts/login/"); err != nil {
		return fmt.Errorf("failed to fetch cookies: %v", err)
	}
	log.Println("✅ Cookies fetched successfully")
	return nil
}

// 🧩 Fetch cookies using headless Chrome
func fetchCookies(loginURL string) error {
	log.Println("🌐 Launching headless Chrome to fetch cookies...")

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Headless,
		chromedp.DisableGPU,
		chromedp.NoSandbox,
	)
	ctx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel = chromedp.NewContext(ctx)
	defer cancel()

	// Navigate & wait for page
	err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(loginURL),
		chromedp.Sleep(15*time.Second),
	)
	if err != nil {
		return fmt.Errorf("navigate failed: %v", err)
	}

	// Fetch cookies
	cookies, err := network.GetCookies().Do(ctx)
	if err != nil {
		return fmt.Errorf("cannot fetch cookies: %v", err)
	}

	f, err := os.Create(cookiesFile)
	if err != nil {
		return fmt.Errorf("cannot create cookies.txt: %v", err)
	}
	defer f.Close()

	_, _ = fmt.Fprintln(f, "# Netscape HTTP Cookie File")
	for _, c := range cookies {
		fmt.Fprintf(f, "%s\t%s\t%s\t%s\t%v\t%s\t%s\n",
			c.Domain, "TRUE", c.Path, boolToStr(c.Secure, "TRUE", "FALSE"), c.Expires, c.Name, c.Value)
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
