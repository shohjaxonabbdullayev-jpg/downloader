# ============================
# 🏗️ Build Go binary
# ============================
FROM golang:1.24.4 AS builder

WORKDIR /app
COPY app/go.mod app/go.sum ./
RUN go mod download
COPY app/ .
RUN go build -o downloader-bot .

# ============================
# 🚀 Final image
# ============================
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/downloader-bot .

ENV PORT=10000
EXPOSE 10000

CMD ["/app/downloader-bot"]
