@echo off
rem Starts the RidePulse demo and opens the dashboard. Double-click to run.
title RidePulse demo
cd /d "%~dp0"

where docker >nul 2>nul
if errorlevel 1 (
  echo Docker isn't installed. Get Docker Desktop from https://www.docker.com/products/docker-desktop/ and run this again.
  pause
  exit /b 1
)
docker info >nul 2>nul
if errorlevel 1 (
  echo Docker isn't running. Start Docker Desktop, wait for it to be ready, and run this again.
  pause
  exit /b 1
)

echo Starting RidePulse. The first run can take several minutes.
docker compose up -d --wait dashboard
if errorlevel 1 (
  echo The demo didn't start. See the output above.
  pause
  exit /b 1
)

start "" http://localhost:8090
echo.
echo RidePulse is running at http://localhost:8090
echo When you're finished, stop it with: docker compose down
pause
