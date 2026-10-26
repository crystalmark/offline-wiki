@echo off
rem Offline Wiki - double-click to start on Windows.
cd /d "%~dp0"
app\offline-wiki-windows-amd64.exe run %*
if errorlevel 1 pause
