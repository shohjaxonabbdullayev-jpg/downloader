# ============================
# 🏗️ STAGE 1 — Build Node.js app
# ============================
FROM node:20-bullseye AS builder

WORKDIR /app

# Copy package.json and package-lock.json
COPY package*.json ./

# Install Node dependencies
RUN npm ci --only=production

# Copy app source
COPY . .

# ==============================
# 🚀 STAGE 2 — Final lightweight image
# ==============================
FROM debian:bookworm-slim

# Install runtime dependencies
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ffmpeg \
        python3 \
        python3-venv \
        python3-pip \
        ca-certificates \
        curl \
        wget \
        git && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# Create Python virtual environment and install yt-dlp + gallery-dl
RUN python3 -m venv /opt/yt && \
    /opt/yt/bin/pip install --upgrade pip && \
    /opt/yt/bin/pip install --no-cache-dir yt-dlp gallery-dl && \
    ln -s /opt/yt/bin/yt-dlp /usr/local/bin/yt-dlp && \
    ln -s /opt/yt/bin/gallery-dl /usr/local/bin/gallery-dl

# Set working directory
WORKDIR /app

# Copy Node.js app from builder stage
COPY --from=builder /app ./

# Copy credential/config files
COPY twitter.txt facebook.txt instagram.txt youtube.txt pinterest.txt ./

# Create downloads directory
RUN mkdir -p downloads

# Set environment variables
ENV PORT=10000
EXPOSE 10000

# Healthcheck
HEALTHCHECK CMD curl -f http://localhost:${PORT}/health || exit 1

# Run the Node.js bot
CMD ["node", "downloader-bot.js"]
