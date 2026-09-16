@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\setup-bridge.ps1"
if errorlevel 1 (
  echo Setup did not finish. Review the error above and run this file again.
  pause
  exit /b 1
)
echo Setup complete. The bridge install folder contains start-bridges.cmd and connect-microsoft.cmd.
pause
