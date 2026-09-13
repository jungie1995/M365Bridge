@echo off
setlocal
if not defined M365BRIDGE_HOME set "M365BRIDGE_HOME=%~dp0"
if not exist "%M365BRIDGE_HOME%\m365-bridge-browser-login.exe" (
  echo The edited M365Bridge browser sign-in helper is not installed here.
  exit /b 1
)
pushd "%M365BRIDGE_HOME%"
if errorlevel 1 exit /b 1
"%M365BRIDGE_HOME%\m365-bridge-browser-login.exe" login-browser
set "result=%errorlevel%"
popd
exit /b %result%
