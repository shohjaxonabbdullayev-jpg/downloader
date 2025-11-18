# ============================
# 🏗️ Node.js + Python Downloader Bot
# ============================
FROM node:20-bullseye

# Set working directory
WORKDIR /app

# Copy Node.js dependency files
COPY package.json package-lock.json* ./

# Install Node.js dependencies (production only)
RUN npm install --production

# Copy app source
COPY . .

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

# Copy credential/config files
COPY twitter.txt facebook.txt instagram.txt youtube.txt pinterest.txt ./

# Create downloads directory
RUN mkdir -p downloads

# Environment variables
ENV PORT=10000
EXPOSE 10000

# Healthcheck
HEALTHCHECK CMD curl -f http://localhost:${PORT}/health || exit 1

# Run Node.js bot
CMD ["node", "downloader-bot.js"]
