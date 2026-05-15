#!/bin/bash
set -e

if [ -z "$1" ]; then
    echo "Usage: deploy.sh <user@server-ip>"
    echo "Example: deploy.sh root@192.168.1.10"
    exit 1
fi

SERVER="$1"
REMOTE_DIR="/tmp/sentrydns-deploy"
PORT="${DNS_PORT:-53}"

echo "==> Building..."
GOOS=linux GOARCH=amd64 go build -o sentrydns cmd/sentrydns/main.go

echo "==> Copying files to server..."
ssh $SERVER "mkdir -p $REMOTE_DIR/data $REMOTE_DIR/scripts"

rsync -avz sentrydns $SERVER:$REMOTE_DIR/
rsync -avz config.yaml $SERVER:$REMOTE_DIR/
rsync -avz data/iran-ranges.txt $SERVER:$REMOTE_DIR/data/
if ssh $SERVER "test -f /opt/sentrydns/data/learned.conf"; then
    ssh $SERVER "cat /opt/sentrydns/data/learned.conf" > /tmp/.remote-learned.conf
    sort -u /tmp/.remote-learned.conf data/learned.conf -o /tmp/.merged-learned.conf
    rsync -avz /tmp/.merged-learned.conf $SERVER:$REMOTE_DIR/data/learned.conf
    rm -f /tmp/.remote-learned.conf /tmp/.merged-learned.conf
else
    rsync -avz data/learned.conf $SERVER:$REMOTE_DIR/data/
fi
rsync -avz scripts/install.sh $SERVER:$REMOTE_DIR/scripts/
rsync -avz scripts/logrotate.conf $SERVER:$REMOTE_DIR/scripts/

echo "==> Running install on server..."
ssh -t $SERVER "cd $REMOTE_DIR && sudo bash scripts/install.sh"

echo "==> Restarting service..."
ssh -t $SERVER "sudo systemctl restart sentrydns"

echo "==> Verifying..."
sleep 2

ssh -t $SERVER "sudo systemctl is-active --quiet sentrydns" \
    && echo "OK: service is running" \
    || { echo "ERROR: service not active"; ssh -t $SERVER "sudo journalctl -u sentrydns -n 20"; exit 1; }

if ssh $SERVER "command -v dig >/dev/null 2>&1"; then
    if ssh $SERVER "dig +short @127.0.0.1 -p $PORT google.com 2>/dev/null | grep -q '^[0-9]'"; then
        echo "OK: sentrydns is responding to queries"
    else
        echo "WARNING: sentrydns not responding on port $PORT"
    fi
fi

echo "==> Cleaning up..."
ssh -t $SERVER "sudo rm -rf $REMOTE_DIR"

echo ""
echo "Deploy complete!"