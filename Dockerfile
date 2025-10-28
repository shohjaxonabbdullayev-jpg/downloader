# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates ffmpeg python3 py3-pip && update-ca-certificates

# Install yt-dlp globally
RUN pip install --no-cache-dir yt-dlp

WORKDIR /app

# Ensure correct Go toolchain
ENV GOTOOLCHAIN=go1.24.4
ENV GOPROXY=https://proxy.golang.org,direct
ENV GOSUMDB=sum.golang.org

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy and build
COPY . .
RUN go build -o main .

# --- FINAL STAGE ---
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache \
    ffmpeg \
    python3 \
    py3-pip \
    ca-certificates \
    && update-ca-certificates

# Some Alpine versions require this symlink for `python`
RUN ln -sf python3 /usr/bin/python

# Install yt-dlp using pip
RUN pip install --no-cache-dir yt-dlp

WORKDIR /app
COPY --from=builder /app/main .

CMD ["./main"]
