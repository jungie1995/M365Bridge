@echo off
setlocal
cd /d "%~dp0"

where go.exe >nul 2>nul
if errorlevel 1 (
  if exist "C:\Program Files\Go\bin\go.exe" (
    set "GO_EXE=C:\Program Files\Go\bin\go.exe"
  ) else (
    echo Go was not found. Install Go 1.27 or add it to PATH.
    pause
    exit /b 1
  )
) else (
  set "GO_EXE=go.exe"
)

echo Building M365Bridge...
"%GO_EXE%" build -o m365-bridge.exe ./cmd/cli
if errorlevel 1 (
  echo Build failed.
  pause
  exit /b 1
)
echo Build complete: %CD%\m365-bridge.exe
endlocal
