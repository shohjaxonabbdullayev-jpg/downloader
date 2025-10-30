# ============================
# 🏗️ STAGE 1 — Build Go binary
# ============================
FROM golang:1.24.4 AS builder

WORKDIR /app

# Install essential build tools
RUN apt-get update && apt-get install -y --no-install-recommends build-essential && \
    rm -rf /var/lib/apt/lists/*

# Download Go dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy and build
COPY . .
RUN go build -o downloader-bot .

# ==============================
# 🚀 STAGE 2 — Final lightweight image
# ==============================
FROM debian:bookworm-slim

# Install system dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ffmpeg \
    python3-full \
    python3-pip \
    ca-certificates \
    curl && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# ✅ Create virtual environment and install yt-dlp + gallery-dl
RUN python3 -m venv /opt/tools && \
