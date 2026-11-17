#!/bin/sh
# First deploy only: creates .env on the server with fresh random secrets.
# Usage: ./init-env.sh <domain-or-ip> <dockerhub-user>
set -eu
cd "$(dirname "$0")"
[ -f .env ] && { echo ".env exists, keeping it"; exit 0; }
rnd() { openssl rand -hex 32; }
sed -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$(rnd)|" \
    -e "s|^REDIS_PASSWORD=.*|REDIS_PASSWORD=$(rnd)|" \
    -e "s|^PEPPER=.*|PEPPER=$(rnd)|" \
    -e "s|^JWT_ACCESS_SECRET=.*|JWT_ACCESS_SECRET=$(rnd)|" \
    -e "s|^JWT_REFRESH_SECRET=.*|JWT_REFRESH_SECRET=$(rnd)|" \
    -e "s|^DOMAIN=.*|DOMAIN=$1|" \
    -e "s|^ALLOWED_ORIGINS=.*|ALLOWED_ORIGINS=https://$1|" \
    -e "s|^DOCKERHUB_USER=.*|DOCKERHUB_USER=$2|" \
    -e "/^DEPLOY_/d" -e "/^HTTP_PORT/d" -e "/^HTTPS_PORT/d" \
    .env.example > .env
chmod 600 .env
echo ".env created. Set BASE_URL and AI_API_KEY in $(pwd)/.env, then: docker compose up -d api"
