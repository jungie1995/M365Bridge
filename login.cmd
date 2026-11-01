@echo off
setlocal
cd /d "%~dp0"
set "M365_CONFIG_FROM_INSTALLATION=1"
if not exist "m365-bridge.exe" call "%~dp0build-m365-bridge.cmd"
if not exist "m365-bridge.exe" exit /b 1
echo Starting Microsoft device-code sign-in...
m365-bridge.exe login
exit /b %errorlevel%
