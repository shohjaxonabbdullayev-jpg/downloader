# Use a lightweight official Go image
FROM golang:1.22-bullseye

# Install yt-dlp and ffmpeg
RUN apt-get update && apt-get install -y ffmpeg curl && \
    curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp && \
    chmod a+rx /usr/local/bin/yt-dlp && \
    rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Copy Go modules first (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy all app files
COPY . .

# Build the app binary
RUN go build -ldflags="-s -w" -o main .

# Expose port for Render health checks
EXPOSE 10000

# Run the app
CMD ["./main"]
