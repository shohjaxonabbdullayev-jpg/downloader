# ============================
# 🏗️ STAGE 1 — Build the Go app
# ============================
FROM golang:1.24.4 AS builder

WORKDIR /app

RUN apt-get update && \
    apt-get install -y --no-install-recommends build-essential && \
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
    python3 \
    python3-pip \
    ca-certificates && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# 🔧 FIX: install yt-dlp with Debian PEP 668 override
RUN pip3 install --break-system-packages --no-cache-dir yt-dlp

WORKDIR /app

COPY --from=builder /app/downloader-bot .
COPY cookies.txt ./cookies.txt
COPY downloads ./downloads

ENV PORT=10000
EXPOSE 10000

CMD ["/app/downloader-bot"]
