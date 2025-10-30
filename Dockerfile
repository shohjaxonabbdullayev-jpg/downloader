# ============================
# 🏗️ STAGE 1 — Build Go binary
# ============================
FROM golang:1.24.4 AS builder

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends build-essential python3 python3-pip && \
    rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o downloader-bot .

# ==============================
# 🚀 STAGE 2 — Final lightweight image
# ==============================
FROM debian:bookworm-slim

# Install dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    ffmpeg \
    python3-full \
    python3-pip \
    ca-certificates \
    git \
    wget && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# ✅ Create virtual environment for Python tools
RUN python3 -m venv /opt/tools && \
    /opt/tools/bin/pip install --no-cache-dir \
        yt-dlp \
        gallery-dl \
        instaloader && \
    ln -s /opt/tools/bin/yt-dlp /usr/local/bin/yt-dlp && \
    ln -s /opt/tools/bin/gallery-dl /usr/local/bin/gallery-dl && \
    ln -s /opt/tools/bin/instaloader /usr/local/bin/instaloader

WORKDIR /app

COPY --from=builder /app/downloader-bot .  
COPY cookies.txt ./cookies.txt  
COPY downloads ./downloads  

ENV PORT=10000
EXPOSE 10000

CMD ["/app/downloader-bot"]
