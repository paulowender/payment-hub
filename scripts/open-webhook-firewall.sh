#!/bin/sh
set -eu
if [ "$(id -u)" -ne 0 ]; then
    echo 'Execute with sudo: sudo sh scripts/open-webhook-firewall.sh' >&2
    exit 1
fi
# Keep all existing firewall rules and Docker networking; open only the HTTPS/ACME ports.
ufw allow 80/tcp comment 'Payment Hub ACME HTTP challenge'
ufw allow 443/tcp comment 'Payment Hub HTTPS webhooks'
ufw status
