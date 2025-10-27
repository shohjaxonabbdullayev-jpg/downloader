# Use stable Go + Debian Bookworm (modern and compatible)
FROM golang:1.24-bookworm

# Install dependencies: Python 3.11+, ffmpeg, curl
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip ffmpeg curl ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Install yt-dlp via pip (Python ≥3.10 required)
RUN pip3 install --no-cache-dir yt-dlp && \
    yt-dlp --version

# Set working directory
WORKDIR /app

# Copy Go modules first (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy all application files
COPY . .

# Build your Go binary
RUN go build -o main .

# Expose the port your app uses
EXPOSE 10000

# Start the app
CMD ["./main"]
