@echo off
setlocal
cd /d "%~dp0"
if not exist "m365-bridge.exe" call "%~dp0build-m365-bridge.cmd"
if not exist "m365-bridge.exe" exit /b 1
echo Starting M365Bridge on http://127.0.0.1:8000
m365-bridge.exe serve --port 8000
endlocal
