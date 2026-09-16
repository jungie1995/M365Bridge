@echo off
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\connect-microsoft.ps1" -InstallRoot "%~dp0."
if errorlevel 1 (
  pause
  exit /b 1
)
