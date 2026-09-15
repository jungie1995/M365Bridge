@echo off
pushd "%~dp0"
"%~dp0m365-bridge-browser-login.exe" login-browser
if errorlevel 1 pause
popd
