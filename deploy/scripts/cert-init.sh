#!/bin/sh
# First Let's Encrypt issuance on the VPS (run once, before `make prod-up`).
# Uses certbot standalone on :80, so nginx must not be running yet.
set -eu
. ./.env
: "${DOMAIN:?set DOMAIN in .env}" "${LETSENCRYPT_EMAIL:?set LETSENCRYPT_EMAIL in .env}"
docker compose -f docker-compose.yml -f docker-compose.prod.yml stop nginx 2>/dev/null || true
docker compose -f docker-compose.yml -f docker-compose.prod.yml run --rm -p 80:80 --entrypoint certbot certbot \
  certonly --standalone --non-interactive --agree-tos -m "$LETSENCRYPT_EMAIL" -d "$DOMAIN"
