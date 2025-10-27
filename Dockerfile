# Use Go on Debian Bookworm base
FROM golang:1.24-bookworm

# Install dependencies: Python 3, pip, ffmpeg, curl
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 python3-pip ffmpeg curl ca-certificates && \
    curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp && \
    chmod a+rx /usr/local/bin/yt-dlp && \
    python3 -m pip install --upgrade pip && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Copy Go modules first (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy the app and cookies.txt
COPY . .

# Ensure downloads directory exists
RUN mkdir -p downloads

# Build the Go app
RUN go build -o main .

EXPOSE 10000
CMD ["./main"]
