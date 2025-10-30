package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

const (
	metubeURL = "http://metube:8081/api" // internal Docker network URL
)

type MeTubeJob struct {
	URL string `json:"url"`
}

type MeTubeResponse struct {
	ID string `json:"id"`
}

func main() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Println("⚠️  No .env file found")
	}

	botToken := os.Getenv("BOT_TOKEN")
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Panic(err)
	}

	log.Printf("🤖 Bot authorized as %s", bot.Self.UserName)
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}
		link := update.Message.Text
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "📥 Downloading your video, please wait...")
		bot.Send(msg)

		videoPath, err := downloadWithMeTube(link)
		if err != nil {
			errMsg := fmt.Sprintf("❌ Error: %v", err)
			bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, errMsg))
			continue
		}

		videoFile := tgbotapi.NewVideo(update.Message.Chat.ID, tgbotapi.FilePath(videoPath))
		videoFile.Caption = "✅ Download complete!"
		if _, err := bot.Send(videoFile); err != nil {
			bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "❌ Failed to send video"))
			continue
		}

		os.Remove(videoPath)
	}
}

func downloadWithMeTube(url string) (string, error) {
	payload := MeTubeJob{URL: url}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(metubeURL+"/download", "application/json", bytes.NewBuffer(body))
	if err != nil {
		return "", fmt.Errorf("failed to contact MeTube: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("MeTube error: %s", data)
	}

	var job MeTubeResponse
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return "", err
	}

	// Wait for MeTube to finish
	videoPath := fmt.Sprintf("./downloads/%s.mp4", job.ID)
	for i := 0; i < 60; i++ {
		if _, err := os.Stat(videoPath); err == nil {
			return videoPath, nil
		}
		time.Sleep(2 * time.Second)
	}

	return "", fmt.Errorf("timeout waiting for MeTube download")
}
