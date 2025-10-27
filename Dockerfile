# Use official Debian-based Go image
FROM golang:1.25-bookworm

# Install dependencies
RUN apt-get update && \
    apt-get install -y ffmpeg curl && \
    curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp && \
    chmod +x /usr/local/bin/yt-dlp

# Set work directory
WORKDIR /app

# Copy source code
COPY . .

# Build the app
RUN go mod download
RUN go build -o app .

# Expose port for Render health checks
EXPOSE 10000

# Run the bot
CMD ["./app"]
