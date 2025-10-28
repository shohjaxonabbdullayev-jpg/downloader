# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates ffmpeg python3 py3-pip py3-wheel py3-setuptools && update-ca-certificates

# Install yt-dlp globally (for use during build)
RUN pip install --no-cache-dir yt-dlp

WORKDIR /app

# Ensure correct Go toolchain
ENV GOTOOLCHAIN=go1.24.4
ENV GOPROXY=https://proxy.golang.org,direct
ENV GOSUMDB=sum.golang.org

# Copy and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build app
RUN go build -o main .

# ---- Final Runtime Stage ----
FROM alpine:3.19

# Install runtime deps
RUN apk add --no-cache ffmpeg python3 py3-pip py3-wheel py3-setuptools ca-certificates && update-ca-certificates

# Symlink python (some scripts use it)
RUN ln -sf python3 /usr/bin/python

# Install yt-dlp again in final image
RUN pip install --no-cache-dir yt-dlp

WORKDIR /app
COPY --from=builder /app/main .

CMD ["./main"]
