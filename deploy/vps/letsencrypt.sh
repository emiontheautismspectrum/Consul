#!/bin/sh
# Switch from the self-signed certificate to Let's Encrypt.
# Works for a domain or a bare IP (IP certs are short-lived, ~6 days, and need the
# "shortlived" ACME profile; certbot renews them automatically every 12h check).
# Usage: set DOMAIN (domain or IP) and LETSENCRYPT_EMAIL in .env, then ./letsencrypt.sh [--dry-run]
set -eu
cd "$(dirname "$0")"
. ./.env
: "${DOMAIN:?set DOMAIN in .env}" "${LETSENCRYPT_EMAIL:?set LETSENCRYPT_EMAIL in .env}"
case "$DOMAIN" in
  *[!0-9.]*) ident="-d $DOMAIN";  profile="" ;;
  *)         ident="--ip-address $DOMAIN"; profile="--preferred-profile shortlived" ;;
esac
docker compose run --rm --entrypoint certbot certbot certonly --webroot -w /var/www/certbot \
  --non-interactive --agree-tos -m "$LETSENCRYPT_EMAIL" --cert-name "$DOMAIN" $ident $profile "$@"
case " $* " in *" --dry-run "*) echo "dry run OK, nothing changed"; exit 0 ;; esac
sed -i '/^SSL_CERT=/d; /^SSL_KEY=/d' .env
printf 'SSL_CERT=/etc/letsencrypt/live/%s/fullchain.pem\nSSL_KEY=/etc/letsencrypt/live/%s/privkey.pem\n' "$DOMAIN" "$DOMAIN" >> .env
docker compose up -d nginx
docker compose --profile letsencrypt up -d certbot
echo "Let's Encrypt enabled for $DOMAIN"
