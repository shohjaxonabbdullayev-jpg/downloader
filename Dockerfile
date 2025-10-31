# ---------- Stage 1: Build Go binary ----------
FROM golang:1.22 AS builder

# Set working directory
WORKDIR /app

# Copy go files
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the Go binary
RUN go build -o bot .

# ---------- Stage 2: Runtime environment ----------
FROM python:3.11-slim

# Install system dependencies (ffmpeg, curl)
RUN apt-get update && apt-get install -y ffmpeg curl && rm -rf /var/lib/apt/lists/*

# Install yt-dlp and gallery-dl
RUN pip install --no-cache-dir yt-dlp gallery-dl

# Create working directory
WORKDIR /app

# Copy compiled binary from builder
COPY --from=builder /app/bot /app/bot

# Copy any additional resources (e.g., cookies.txt if exists)
COPY cookies.txt /app/cookies.txt
COPY .env /app/.env

# Create downloads directory
RUN mkdir -p /app/downloads

# Expose the health check port
EXPOSE 10000

# Run the bot
CMD ["./bot"]

