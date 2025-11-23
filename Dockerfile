# 1. Use official Go image for build
FROM golang:1.21-alpine AS builder

# Install git for fetching modules
RUN apk add --no-cache git

WORKDIR /app

# Copy go.mod and go.sum first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build Go binary for Linux (no CGO)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o downloader-bot .

# ===================== Final Image =====================
FROM alpine:3.18

# Install dependencies for running your bot
RUN apk add --no-cache bash ca-certificates ffmpeg python3 py3-pip

WORKDIR /app
COPY --from=builder /app/downloader-bot .

# Copy downloads folder (optional)
RUN mkdir downloads

# Expose port if using webhooks/health
EXPOSE 8080

# Set environment variable for Telegram token
ENV BOT_TOKEN=your-bot-token

CMD ["./downloader-bot"]
