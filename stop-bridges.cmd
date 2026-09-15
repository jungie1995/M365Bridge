@echo off
taskkill /IM m365-bridge.exe /F >nul 2>&1
taskkill /IM m365-bridge-images.exe /F >nul 2>&1
echo Content Studio bridges stopped.
