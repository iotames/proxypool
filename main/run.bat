@echo off
chcp 65001 >nul
cd /d "%~dp0"
if not exist proxypool.exe (
  echo [run.bat] proxypool.exe not found. Please run "make build" first.
  exit /b 1
)
proxypool.exe --port=1080 --conf=clash.yaml