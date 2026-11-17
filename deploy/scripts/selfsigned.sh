#!/bin/sh
# Self-signed certificate for local HTTPS (production uses Let's Encrypt).
set -eu
d=deploy/nginx/certs
[ -f "$d/fullchain.pem" ] && exit 0
mkdir -p "$d"
openssl req -x509 -nodes -newkey rsa:2048 -days 365 -subj "/CN=localhost" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" \
  -keyout "$d/privkey.pem" -out "$d/fullchain.pem" 2>/dev/null
chmod 644 "$d/privkey.pem"
echo "self-signed certificate created in $d"
