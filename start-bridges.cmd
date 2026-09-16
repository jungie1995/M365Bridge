@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\start-bridge.ps1" -InstallRoot "%~dp0." -ConnectIfNeeded
if errorlevel 1 (
  pause
  exit /b 1
)
endlocal
