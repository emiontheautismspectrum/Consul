#!/bin/sh
# Idempotently applies host hardening on the VPS (run via `make vps-host`):
# sshd key-only login, nftables firewall + rate limits, fail2ban, journald size cap.
# The firewall is applied with a 3-minute auto-rollback that `make vps-host` cancels
# only after a fresh SSH login succeeds.
set -eu
cd "$(dirname "$0")"
sudo install -m 600 sshd-hardening.conf /etc/ssh/sshd_config.d/00-hardening.conf
sudo sshd -t && sudo systemctl reload sshd
sudo install -m 644 host-firewall.nft /etc/nftables/host-firewall.nft
sudo install -m 644 host-firewall.service /etc/systemd/system/host-firewall.service
sudo install -m 644 jail.local /etc/fail2ban/jail.local
sudo install -m 644 finadvisor-nginx-limit.conf /etc/fail2ban/filter.d/finadvisor-nginx-limit.conf
sudo restorecon -q /etc/ssh/sshd_config.d/00-hardening.conf /etc/nftables/host-firewall.nft \
  /etc/systemd/system/host-firewall.service /etc/fail2ban/jail.local /etc/fail2ban/filter.d/finadvisor-nginx-limit.conf || true
# Cap the systemd journal (nginx logs go there for fail2ban); the disk is 10 GB.
sudo install -D -m 644 journald-size.conf /etc/systemd/journald.conf.d/size.conf
sudo systemctl restart systemd-journald
sudo nft -c -f /etc/nftables/host-firewall.nft
sudo systemctl daemon-reload
sudo systemctl stop fw-rollback.timer 2>/dev/null || true
sudo systemd-run --quiet --unit=fw-rollback --on-active=180 /usr/sbin/nft delete table inet host_fw
sudo systemctl enable -q host-firewall && sudo systemctl reload-or-restart host-firewall
sudo fail2ban-client -t >/dev/null && sudo systemctl enable -q fail2ban && sudo systemctl reload-or-restart fail2ban
echo "host config applied; rollback armed"
