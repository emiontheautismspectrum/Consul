#!/bin/sh
# Creates .env from .env.example with random secrets (keeps an existing .env untouched).
set -eu
[ -f .env ] && { echo ".env exists, skipping"; exit 0; }
rnd() { openssl rand -hex 32; }
sed -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$(rnd)|" \
    -e "s|^REDIS_PASSWORD=.*|REDIS_PASSWORD=$(rnd)|" \
    -e "s|^PEPPER=.*|PEPPER=$(rnd)|" \
    -e "s|^JWT_ACCESS_SECRET=.*|JWT_ACCESS_SECRET=$(rnd)|" \
    -e "s|^JWT_REFRESH_SECRET=.*|JWT_REFRESH_SECRET=$(rnd)|" \
    .env.example > .env
echo ".env created — set BASE_URL and AI_API_KEY for the AI advisor"
