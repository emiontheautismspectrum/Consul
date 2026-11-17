#!/bin/sh
# Switch from the self-signed certificate to Let's Encrypt once DOMAIN points to this server.
# Usage: set DOMAIN and LETSENCRYPT_EMAIL in .env, then ./letsencrypt.sh
set -eu
cd "$(dirname "$0")"
. ./.env
: "${DOMAIN:?}" "${LETSENCRYPT_EMAIL:?set LETSENCRYPT_EMAIL in .env}"
docker compose run --rm --entrypoint certbot certbot certonly --webroot -w /var/www/certbot \
  --non-interactive --agree-tos -m "$LETSENCRYPT_EMAIL" -d "$DOMAIN"
grep -q '^SSL_CERT=' .env || printf 'SSL_CERT=/etc/letsencrypt/live/%s/fullchain.pem\nSSL_KEY=/etc/letsencrypt/live/%s/privkey.pem\n' "$DOMAIN" "$DOMAIN" >> .env
docker compose up -d nginx
docker compose --profile letsencrypt up -d certbot
echo "Let's Encrypt enabled for $DOMAIN"
