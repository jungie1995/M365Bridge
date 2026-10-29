@echo off
setlocal
cd /d "%~dp0"
if not exist "m365-bridge.exe" call "%~dp0build-m365-bridge.cmd"
if not exist "m365-bridge.exe" exit /b 1

echo Step 1 of 2: sign in to the Microsoft account that has Copilot access.
m365-bridge.exe login-browser
if errorlevel 1 (
  echo Microsoft sign-in did not complete. The bridge was not started.
  pause
  exit /b 1
)

echo Step 2 of 2: starting M365Bridge on http://127.0.0.1:8000
m365-bridge.exe serve --port 8000
endlocal
