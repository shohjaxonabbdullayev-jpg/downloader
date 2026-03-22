# Encode a Netscape cookie file for Render env vars (after you export youtube.txt etc. yourself).
# Usage (from repo root):
#   .\scripts\encode-cookies-b64.ps1
#   .\scripts\encode-cookies-b64.ps1 -Platform instagram
param(
    [ValidateSet('youtube', 'instagram', 'twitter', 'facebook', 'pinterest')]
    [string]$Platform = 'youtube'
)

$names = @{
    youtube   = @{ File = 'youtube.txt';   Env = 'YOUTUBE_COOKIES_B64' }
    instagram = @{ File = 'instagram.txt'; Env = 'INSTAGRAM_COOKIES_B64' }
    twitter   = @{ File = 'twitter.txt';   Env = 'TWITTER_COOKIES_B64' }
    facebook  = @{ File = 'facebook.txt';  Env = 'FACEBOOK_COOKIES_B64' }
    pinterest = @{ File = 'pinterest.txt'; Env = 'PINTEREST_COOKIES_B64' }
}

$root = Split-Path -Parent $PSScriptRoot

$meta = $names[$Platform]
$path = Join-Path $root $meta.File

if (-not (Test-Path -LiteralPath $path)) {
    Write-Error "Missing file: $path`nExport cookies with a browser extension (see README), save as $($meta.File) in the repo root, then run this script again."
    exit 1
}

$bytes = [IO.File]::ReadAllBytes($path)
if ($bytes.Length -eq 0) {
    Write-Error "File is empty: $path"
    exit 1
}

$b64 = [Convert]::ToBase64String($bytes)

Write-Host ""
Write-Host "Render -> Environment -> add:"
Write-Host "  Name : $($meta.Env)"
Write-Host "  Value: (single line below)"
Write-Host ""
Write-Host $b64
Write-Host ""

try {
    Set-Clipboard -Value $b64
    Write-Host "(Also copied to clipboard.)" -ForegroundColor Green
} catch {
    Write-Host "(Clipboard not available; copy the line above manually.)" -ForegroundColor Yellow
}
