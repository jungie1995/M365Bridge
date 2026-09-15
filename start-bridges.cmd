@echo off
setlocal
cd /d "%~dp0"
echo Starting Content Studio text bridge on http://127.0.0.1:8000 ...
start "M365 Text Bridge" /min "%~dp0text-runtime\m365-bridge.exe" serve --port 8000
echo Starting Content Studio image bridge on http://127.0.0.1:8001 ...
start "M365 Image Bridge" /min "%~dp0image-runtime\m365-bridge-images.exe" serve --port 8001
echo Both bridge processes were launched. Keep this window open only if you want to read this message.
timeout /t 3 /nobreak >nul
curl.exe -s --max-time 3 http://127.0.0.1:8000/health >nul && echo Text bridge: OK || echo Text bridge: NOT READY
curl.exe -s --max-time 3 http://127.0.0.1:8001/health >nul && echo Image bridge: OK || echo Image bridge: NOT READY
endlocal
