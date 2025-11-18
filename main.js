// instagram_downloader.js

const fs = require('fs');
const path = require('path');
const axios = require('axios');

// Optional: Load cookies from cookies.txt
const COOKIES_FILE = './cookies.txt';
let COOKIES = '';
if (fs.existsSync(COOKIES_FILE)) {
    COOKIES = fs.readFileSync(COOKIES_FILE, 'utf8').replace(/\n/g, '; ');
}

// Helper: download file
async function downloadFile(url, outputDir, filename) {
    const filePath = path.join(outputDir, filename);
    const writer = fs.createWriteStream(filePath);

    const response = await axios({
        url,
        method: 'GET',
        responseType: 'stream',
        headers: COOKIES ? { Cookie: COOKIES, 'User-Agent': 'Mozilla/5.0' } : { 'User-Agent': 'Mozilla/5.0' }
    });

    response.data.pipe(writer);

    return new Promise((resolve, reject) => {
        writer.on('finish', () => resolve(filePath));
        writer.on('error', reject);
    });
}

// Extract media URLs from Instagram HTML
function extractMediaUrls(html) {
    const urls = [];

    // Images
    const imgRegex = /"display_url":"([^"]+)"/g;
    let match;
    while ((match = imgRegex.exec(html)) !== null) {
        urls.push(match[1].replace(/\\u0026/g, '&'));
    }

    // Videos
    const videoRegex = /"video_url":"([^"]+)"/g;
    while ((match = videoRegex.exec(html)) !== null) {
        urls.push(match[1].replace(/\\u0026/g, '&'));
    }

    return [...new Set(urls)];
}

// Main download function (like “Download” button in userscript)
async function downloadInstagram(url, outputDir = './downloads') {
    if (!fs.existsSync(outputDir)) fs.mkdirSync(outputDir, { recursive: true });

    console.log(`Fetching Instagram page: ${url}`);
    const response = await axios.get(url, {
        headers: COOKIES ? { Cookie: COOKIES, 'User-Agent': 'Mozilla/5.0' } : { 'User-Agent': 'Mozilla/5.0' }
    });

    const html = response.data;
    const mediaUrls = extractMediaUrls(html);

    if (mediaUrls.length === 0) {
        console.log('No media found.');
        return;
    }

    console.log(`Found ${mediaUrls.length} media items. Downloading...`);
    for (let i = 0; i < mediaUrls.length; i++) {
        const mediaUrl = mediaUrls[i];
        const ext = mediaUrl.includes('.mp4') ? '.mp4' : '.jpg';
        const filename = `instagram_${Date.now()}_${i}${ext}`;
        await downloadFile(mediaUrl, outputDir, filename);
        console.log(`Downloaded: ${filename}`);
    }

    console.log('All downloads completed!');
}

// Example usage:
const exampleURL = 'https://www.instagram.com/p/POST_ID/'; // Replace with real post URL
downloadInstagram(exampleURL).catch(console.error);
