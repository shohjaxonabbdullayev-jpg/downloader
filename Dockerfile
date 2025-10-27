# Use lightweight Go image
FROM golang:1.24-bullseye

# Install yt-dlp and ffmpeg
RUN apt-get update && apt-get install -y ffmpeg curl && \
    curl -L https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp -o /usr/local/bin/yt-dlp && \
    chmod a+rx /usr/local/bin/yt-dlp

# Set working directory
WORKDIR /app

# Copy Go modules first (for caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy the app and cookies
COPY . .

# Ensure downloads directory exists
RUN mkdir -p downloads

# Build the Go app
RUN go build -o main .

EXPOSE 10000
CMD ["./main"]
