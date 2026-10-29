@echo off
setlocal
cd /d "%~dp0"
if not exist "m365-bridge.exe" call "%~dp0build-m365-bridge.cmd"
if not exist "m365-bridge.exe" exit /b 1
echo Starting zero-browser Microsoft sign-in...
m365-bridge.exe login
endlocal
