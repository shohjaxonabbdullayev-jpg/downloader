package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	ffmpegPath          = "ffmpeg"
	ytDlpPath           = "yt-dlp"
	galleryDlPath       = "gallery-dl"
	maxVideoHeight      = 720
	telegramMaxFileSize = 50 * 1024 * 1024 // 50 MB bot upload limit
	minFileSizeBytes    = 1024              // ignore tiny files (thumbnails etc.)
)

var (
	downloadsDir = "downloads"
	sem          = make(chan struct{}, 3) // concurrency limit
)

// ============================================================
//                            MAIN
// ============================================================
func main() {
	_ = godotenv.Load()

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		log.Fatal("❌ BOT_TOKEN missing")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	os.MkdirAll(downloadsDir, 0755)

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("🤖 Bot running as @%s", bot.Self.UserName)

	// Health check server
	go func() {
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			fmt.Fprint(w, "OK")
		})
		log.Printf("💚 Health check server on port %s", port)
		log.Fatal(http.ListenAndServe(":"+port, nil))
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			go handleMessage(bot, update.Message)
		}
	}
}

// ============================================================
//                        HANDLE MESSAGE
// ============================================================
func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	chatID := msg.Chat.ID

	if text == "/start" {
		bot.Send(tgbotapi.NewMessage(chatID,
			fmt.Sprintf("👋 Salom %s!\n\n🎥 Instagram, TikTok, Pinterest, Facebook yoki X (Twitter) link yuboring — videoni yoki rasmni yuboraman.",
				msg.From.FirstName)))
		return
	}

	links := extractLinks(text)
	if len(links) == 0 {
		return
	}

	waitMsg, _ := bot.Send(tgbotapi.NewMessage(chatID, "⏳ Yuklanmoqda..."))

	for _, link := range links {
		go func(l string) {
			sem <- struct{}{}
			files, mediaType, err := download(l)
			<-sem

			// try to delete the "loading" message once
			_ = bot.Request(tgbotapi.DeleteMessageConfig{
				ChatID:    chatID,
				MessageID: waitMsg.MessageID,
			})

			if err != nil || len(files) == 0 {
				if err != nil {
					log.Printf("download error for %s: %v", l, err)
				}
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Yuklab bo‘lmadi. Linkni tekshiring."))
				return
			}

			// send the selected files (usually one main file)
			for _, f := range files {
				log.Printf("Sending file: %s (mediaType=%s)", f, mediaType)
				sendMedia(bot, chatID, f, msg.MessageID, mediaType)
				os.Remove(f)
			}
		}(link)
	}
}

// ============================================================
//                          LINK PARSING
// ============================================================
func extractLinks(text string) []string {
	re := regexp.MustCompile(`https?://\S+`)
	all := re.FindAllString(text, -1)
	var out []string
	for _, u := range all {
		if isSupported(u) {
			out = append(out, u)
		}
	}
	return out
}

func isSupported(u string) bool {
	u = strings.ToLower(u)
	return strings.Contains(u, "instagram") ||
		strings.Contains(u, "instagr.am") ||
		strings.Contains(u, "pinterest") ||
		strings.Contains(u, "pin.it") ||
		strings.Contains(u, "tiktok") ||
		strings.Contains(u, "facebook") ||
		strings.Contains(u, "fb.watch") ||
		strings.Contains(u, "twitter") ||
		strings.Contains(u, "x.com")
}

// ============================================================
//                          DOWNLOAD
// ============================================================
func download(link string) ([]string, string, error) {
	start := time.Now()
	out := filepath.Join(downloadsDir, fmt.Sprintf("%d_%%(title)s.%%(ext)s", time.Now().Unix()))
	var args []string

	switch {
	case strings.Contains(link, "instagram"):
		args = []string{"--no-warnings", "-f", "best", "-o", out, link}
		if fileExists("instagram.txt") {
			args = append([]string{"--cookies", "instagram.txt"}, args...)
		}
	default:
		args = []string{"--no-warnings", "-f", "bestvideo+bestaudio/best", "--merge-output-format", "mp4", "-o", out, link}

		if strings.Contains(link, "pinterest") && fileExists("pinterest.txt") {
			args = append([]string{"--cookies", "pinterest.txt"}, args...)
		}
		if (strings.Contains(link, "twitter") || strings.Contains(link, "x.com")) && fileExists("twitter.txt") {
			args = append([]string{"--cookies", "twitter.txt"}, args...)
		}
		if strings.Contains(link, "facebook") && fileExists("facebook.txt") {
			args = append([]string{"--cookies", "facebook.txt"}, args...)
		}
	}

	// Run yt-dlp and capture output (helpful for debugging)
	outText, err := run(ytDlpPath, args...)
	if err != nil {
		// log detailed yt-dlp output for debugging
		log.Printf("yt-dlp error: %v\noutput:\n%s", err, outText)
		// continue to check gallery-dl fallback
	} else {
		log.Printf("yt-dlp output:\n%s", outText)
	}

	// collect new files and pick best media
	files := recentFiles(start)
	bestFiles, mediaType := selectBestMedia(files)
	if len(bestFiles) > 0 {
		return bestFiles, mediaType, nil
	}

	// fallback to gallery-dl (images)
	gOut, gErr := run(galleryDlPath, "-d", downloadsDir, link)
	if gErr != nil {
		log.Printf("gallery-dl error: %v\noutput:\n%s", gErr, gOut)
	} else {
		log.Printf("gallery-dl output:\n%s", gOut)
	}
	files = recentFiles(start)
	bestFiles, mediaType = selectBestMedia(files)
	if len(bestFiles) > 0 {
		return bestFiles, mediaType, nil
	}

	return nil, "", fmt.Errorf("download failed (yt-dlp/gallery-dl produced no files)")
}

// ============================================================
//                        EXEC COMMAND
// ============================================================
func run(cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	return buf.String(), err
}

// ============================================================
//                        RECENT FILES
// ============================================================
func recentFiles(since time.Time) []string {
	var files []string
	_ = filepath.Walk(downloadsDir, func(p string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && info.ModTime().After(since) && info.Size() >= minFileSizeBytes {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// ============================================================
//                     SELECT BEST MEDIA FILES
// ============================================================
// Choose the main video if present (largest video). Otherwise return images (all).
func selectBestMedia(paths []string) ([]string, string) {
	if len(paths) == 0 {
		return nil, ""
	}

	// normalize extension lists
	videoExts := map[string]bool{
		".mp4": true, ".mov": true, ".webm": true, ".mkv": true,
		".flv": true, ".avi": true, ".mpg": true, ".mpeg": true, ".m4v": true, ".3gp": true,
	}
	imgExts := map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true,
	}

	// find largest video file
	var largestVideo string
	var largestVideoSize int64
	var images []string
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if videoExts[ext] {
			if info.Size() > largestVideoSize {
				largestVideoSize = info.Size()
				largestVideo = p
			}
		}
		if imgExts[ext] {
			images = append(images, p)
		}
	}

	if largestVideo != "" {
		// return single best video file
		return []string{largestVideo}, "video"
	}

	// if no video, but images present, return images (as separate files)
	if len(images) > 0 {
		// sort images by name (stable)
		sort.Strings(images)
		return images, "image"
	}

	// fallback: if nothing classified, return all files as documents
	sort.Strings(paths)
	return paths, "file"
}

// ============================================================
//                        SEND MEDIA (ADVANCED)
// ============================================================
func sendMedia(bot *tgbotapi.BotAPI, chatID int64, filePath string, replyTo int, mediaType string) {
	caption := "@downloaderin123_bot orqali yuklab olindi"

	fi, err := os.Stat(filePath)
	if err != nil {
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Faylni o‘qib bo‘lmadi."))
		return
	}
	size := fi.Size()
	ext := strings.ToLower(filepath.Ext(filePath))

	videoExts := map[string]bool{".mp4": true, ".mov": true, ".webm": true, ".mkv": true, ".flv": true, ".avi": true, ".mpg": true, ".mpeg": true, ".m4v": true, ".3gp": true}
	imgExts := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}
	gifExts := map[string]bool{".gif": true}

	// GIF or webm -> convert
	if gifExts[ext] || ext == ".webm" {
		tmp := filePath + ".converted.mp4"
		if err := convertToMP4(filePath, tmp); err == nil {
			os.Remove(filePath)
			filePath = tmp
			fi, _ = os.Stat(filePath)
			size = fi.Size()
			ext = ".mp4"
			mediaType = "video"
		} else {
			log.Printf("convertToMP4 failed: %v", err)
		}
	}

	// Video: compress if needed
	if mediaType == "video" || videoExts[ext] {
		if size > telegramMaxFileSize {
			tmp := filePath + ".compressed.mp4"
			if err := compressVideoToLimit(filePath, tmp, telegramMaxFileSize); err == nil {
				os.Remove(filePath)
				filePath = tmp
				fi, _ = os.Stat(filePath)
				size = fi.Size()
			} else {
				log.Printf("compressVideoToLimit couldn't compress below limit: %v", err)
			}
		}

		// Still too big → upload to transfer.sh
		if size > telegramMaxFileSize {
			url, err := uploadToTransferSh(filePath)
			if err != nil {
				log.Printf("uploadToTransferSh failed: %v", err)
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Fayl juda katta va yuklab bo‘lmadi."))
			} else {
				bot.Send(tgbotapi.NewMessage(chatID,
					"📦 Fayl juda katta, shuning uchun yuklab qo‘ydim:\n"+url))
			}
			return
		}

		// Send video
		v := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(filePath))
		v.Caption = caption
		v.ReplyToMessageID = replyTo
		if _, err := bot.Send(v); err != nil {
			log.Printf("Send video failed: %v", err)
			// Fallback to document
			d := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
			d.Caption = caption
			d.ReplyToMessageID = replyTo
			if _, err := bot.Send(d); err != nil {
				log.Printf("Fallback send document failed: %v", err)
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Mediani yuborishda xatolik yuz berdi."))
			}
		}
		return
	}

	// Image sending
	if imgExts[ext] {
		p := tgbotapi.NewPhoto(chatID, tgbotapi.FilePath(filePath))
		p.Caption = caption
		p.ReplyToMessageID = replyTo
		if _, err := bot.Send(p); err != nil {
			log.Printf("Send photo failed: %v", err)
			d := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
			d.Caption = caption
			d.ReplyToMessageID = replyTo
			if _, err := bot.Send(d); err != nil {
				log.Printf("Send photo fallback failed: %v", err)
				bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Rasmni yuborishda xatolik yuz berdi."))
			}
		}
		return
	}

	// Unknown → send as document
	d := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(filePath))
	d.Caption = caption
	d.ReplyToMessageID = replyTo
	if _, err := bot.Send(d); err != nil {
		log.Printf("Send document failed: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Faylni yuborishda xatolik yuz berdi."))
	}
}

// ============================================================
//                 VIDEO CONVERSION + COMPRESSION
// ============================================================
func convertToMP4(in, out string) error {
	args := []string{
		"-y", "-i", in,
		// scale by height limit (preserve aspect)
		"-vf", fmt.Sprintf("scale='if(gt(iw,ih),-2,%d)':'if(gt(ih,iw),-2,%d)'", maxVideoHeight, maxVideoHeight),
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "28",
		"-c:a", "aac", "-b:a", "128k",
		out,
	}
	_, err := run(ffmpegPath, args...)
	return err
}

func compressVideoToLimit(in, out string, limit int64) error {
	for crf := 28; crf <= 46; crf += 2 {
		args := []string{
			"-y", "-i", in,
			"-vf", fmt.Sprintf("scale='if(gt(iw,ih),-2,%d)':'if(gt(ih,iw),-2,%d)'", maxVideoHeight, maxVideoHeight),
			"-c:v", "libx264", "-preset", "veryfast", "-crf", strconv.Itoa(crf),
			"-c:a", "aac", "-b:a", "96k",
			out,
		}
		_, err := run(ffmpegPath, args...)
		if err != nil {
			log.Printf("ffmpeg attempt crf=%d failed: %v", crf, err)
			continue
		}
		fi, err := os.Stat(out)
		if err == nil && fi.Size() <= limit {
			return nil
		}
		// otherwise try next crf
	}
	return fmt.Errorf("cannot compress enough")
}

// ============================================================
//                    LARGE FILE UPLOAD FALLBACK
// ============================================================
func uploadToTransferSh(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	fi, _ := f.Stat()
	fileName := filepath.Base(path)
	url := "https://transfer.sh/" + fileName

	req, err := http.NewRequest("PUT", url, f)
	if err != nil {
		return "", err
	}

	req.ContentLength = fi.Size()
	req.Header.Set("User-Agent", "downloader-bot")
	req.Header.Set("Content-Type", mime.TypeByExtension(filepath.Ext(fileName)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload failed: %s", string(b))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(body)), nil
}

// ============================================================
//                          HELPERS
// ============================================================
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
