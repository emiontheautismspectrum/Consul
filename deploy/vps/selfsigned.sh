#!/bin/sh
# Self-signed certificate for an IP/host until a domain with Let's Encrypt is attached.
set -eu
cd "$(dirname "$0")"
. ./.env
d=nginx/certs
[ -f "$d/fullchain.pem" ] && { echo "certificate exists"; exit 0; }
mkdir -p "$d"
case "$DOMAIN" in
  *[!0-9.]*) san="DNS:$DOMAIN" ;;
  *)         san="IP:$DOMAIN" ;;
esac
openssl req -x509 -nodes -newkey rsa:2048 -days 825 -subj "/CN=$DOMAIN" -addext "subjectAltName=$san" \
  -keyout "$d/privkey.pem" -out "$d/fullchain.pem" 2>/dev/null
chmod 644 "$d/privkey.pem"
echo "self-signed certificate for $DOMAIN created"
