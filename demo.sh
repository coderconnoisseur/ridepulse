#!/bin/sh
# Starts the RidePulse demo and opens the dashboard.
cd "$(dirname "$0")" || exit 1

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker isn't installed. Get Docker from https://www.docker.com/products/docker-desktop/ and run this again."
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "Docker isn't running. Start it, wait for it to be ready, and run this again."
  exit 1
fi

echo "Starting RidePulse. The first run can take several minutes."
docker compose up -d --wait dashboard || { echo "The demo didn't start. See the output above."; exit 1; }

url=http://localhost:8090
if command -v open >/dev/null 2>&1; then open "$url"
elif command -v xdg-open >/dev/null 2>&1; then xdg-open "$url"
fi
echo
echo "RidePulse is running at $url"
echo "When you're finished, stop it with: docker compose down"
