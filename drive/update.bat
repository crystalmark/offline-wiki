@echo off
rem Offline Wiki - download or refresh libraries, models and programs (needs internet).
cd /d "%~dp0"
app\offline-wiki-windows-amd64.exe update %*
pause
