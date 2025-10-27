# Use Go on Debian 12 (Bookworm) base for Python 3.11 support
FROM golang:1.24-bookworm

# Install dependencies: Python, pip, ffmpeg, curl
RUN apt-get update && apt-get install -y \
    python3 python3-pip ffmpeg curl && \
    curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp && \
    chmod a+rx /usr/local/bin/yt-dlp && \
    python3 -m pip install --upgrade pip

# Set working directory
WORKDIR /app

# Copy Go modules first (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy the app code and cookies.txt
COPY . .

# Ensure downloads directory exists
RUN mkdir -p downloads

# Build the Go app
RUN go build -o main .

# Expose port
EXPOSE 10000

# Start the bot
CMD ["./main"]
