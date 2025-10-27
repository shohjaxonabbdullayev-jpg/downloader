# Use stable Go + Debian Bookworm (newer repo than bullseye)
FROM golang:1.24-bookworm

# Install dependencies: ffmpeg + curl + yt-dlp
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg curl ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp && \
    chmod a+rx /usr/local/bin/yt-dlp && \
    yt-dlp --version

# Set working directory
WORKDIR /app

# Copy Go modules first (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy app source code
COPY . .

# Build binary
RUN go build -o main .

# Expose port
EXPOSE 10000

# Run the app
CMD ["./main"]
