# ========= Stage 1: Build Go binary =========
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install required tools
RUN apk add --no-cache git

# Copy Go modules and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the Go app statically for Linux
RUN go build -o downloader-bot .

# ========= Stage 2: Runtime Environment =========
FROM alpine:latest

WORKDIR /app

# Install ffmpeg, python3, and pip for yt-dlp
RUN apk add --no-cache ffmpeg python3 py3-pip ca-certificates && \
    update-ca-certificates && \
    pip install --no-cache-dir yt-dlp

# Copy the built Go binary and other necessary files
COPY --from=builder /app/downloader-bot .
COPY .env .env
COPY cookies.txt cookies.txt
COPY downloads ./downloads

# Create folder if not exists
RUN mkdir -p /app/downloads

# Expose health check port
EXPOSE 8080

# Set environment variables
ENV PATH="/usr/local/bin:${PATH}"

# Run the bot
CMD ["./downloader-bot"]
