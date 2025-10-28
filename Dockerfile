# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates ffmpeg python3 py3-pip && update-ca-certificates

# Install yt-dlp globally
RUN pip install --no-cache-dir yt-dlp

WORKDIR /app

# Ensure correct Go toolchain for your version
ENV GOTOOLCHAIN=go1.24.4
ENV GOPROXY=https://proxy.golang.org,direct
ENV GOSUMDB=sum.golang.org

# Copy dependency files
COPY go.mod go.sum ./

# Download Go dependencies
RUN go mod download

# Copy the source code
COPY . .

# Build Go binary
RUN go build -o main .

# Final minimal image
FROM alpine:latest

# Install only runtime dependencies
RUN apk add --no-cache ffmpeg python3 py3-pip ca-certificates && update-ca-certificates \
    && pip install --no-cache-dir yt-dlp

WORKDIR /app
COPY --from=builder /app/main .

CMD ["./main"]
