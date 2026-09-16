@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0connect-microsoft.ps1"
if errorlevel 1 (
  pause
  exit /b 1
)
