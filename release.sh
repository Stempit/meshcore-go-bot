#!/usr/bin/env bash
set -euo pipefail

TARGET=root@10.0.3.12

echo "Building for linux/arm64..."
GOOS=linux GOARCH=arm64 go build -o bot ./cmd/bot

echo "Stopping service..."
ssh "$TARGET" systemctl stop meshcore-bot

echo "Uploading..."
scp bot "$TARGET:/root/meshbot/bot"

echo "Starting service..."
ssh "$TARGET" systemctl start meshcore-bot

echo "Done."
