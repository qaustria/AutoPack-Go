#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    exec sudo "$0" "$@"
fi

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
case "$(uname -m)" in
    aarch64|arm64)
        source_binary="$repo_dir/prebuilt/cone-linux-arm64"
        ;;
    armv7l|armv7*)
        source_binary="$repo_dir/prebuilt/cone-linux-armv7"
        ;;
    *)
        echo "Unsupported Raspberry Pi architecture: $(uname -m)" >&2
        exit 1
        ;;
esac

if [ ! -f "$source_binary" ]; then
    echo "Missing prebuilt Cone binary: $source_binary" >&2
    exit 1
fi

if ! command -v nginx >/dev/null 2>&1; then
    echo "nginx is required for the Cone Pi deployment." >&2
    echo "Install it with: sudo apt-get update && sudo apt-get install -y nginx" >&2
    exit 1
fi

nginx_limits=/etc/nginx/conf.d/cone-limits.conf
nginx_site=/etc/nginx/sites-available/qstr.xyz
nginx_limits_backup=""
nginx_site_backup=""
if [ -f "$nginx_limits" ]; then
    nginx_limits_backup=$(mktemp /tmp/cone-nginx-limits-backup.XXXXXX)
    cp "$nginx_limits" "$nginx_limits_backup"
fi
if [ -f "$nginx_site" ]; then
    nginx_site_backup=$(mktemp /tmp/cone-nginx-site-backup.XXXXXX)
    cp "$nginx_site" "$nginx_site_backup"
fi
install -d -m 0755 /etc/nginx/conf.d
install -d -m 0755 /etc/nginx/sites-available /etc/nginx/sites-enabled
install -m 0644 "$repo_dir/deploy/nginx/cone-limits.conf" "$nginx_limits"
install -m 0644 "$repo_dir/deploy/nginx/qstr.xyz" "$nginx_site"
ln -sfn "$nginx_site" /etc/nginx/sites-enabled/qstr.xyz
if ! nginx -t; then
    if [ -n "$nginx_limits_backup" ]; then
        install -m 0644 "$nginx_limits_backup" "$nginx_limits"
    else
        rm -f "$nginx_limits"
    fi
    if [ -n "$nginx_site_backup" ]; then
        install -m 0644 "$nginx_site_backup" "$nginx_site"
    else
        rm -f "$nginx_site" /etc/nginx/sites-enabled/qstr.xyz
    fi
    rm -f "$nginx_limits_backup" "$nginx_site_backup"
    echo "Cone nginx configuration failed validation and was rolled back." >&2
    exit 1
fi
rm -f "$nginx_limits_backup" "$nginx_site_backup"

# Older installations may also have a per-user Cone unit. Stop and disable it
# before starting the system unit so an outdated process cannot retain port 8080.
service_user=$(systemctl show cone -p User --value 2>/dev/null || true)
if [ -n "$service_user" ] && service_uid=$(id -u "$service_user" 2>/dev/null); then
    runuser -u "$service_user" -- env XDG_RUNTIME_DIR="/run/user/$service_uid" \
        systemctl --user disable --now cone.service >/dev/null 2>&1 || true
fi

systemctl stop cone
install -m 0755 "$source_binary" "$repo_dir/cone"
install -d -m 0755 /etc/systemd/system/cone.service.d
install -m 0644 "$repo_dir/deploy/systemd/cone-pi.conf" /etc/systemd/system/cone.service.d/20-cone-network.conf
systemctl daemon-reload
systemctl start cone

i=0
while [ "$i" -lt 20 ]; do
    if health=$(curl -fsS http://127.0.0.1:8080/healthz 2>/dev/null); then
        printf '%s\n' "$health"
        case "$health" in
            *'"version":"1.4.5"'*) break ;;
        esac
    fi
    i=$((i + 1))
    sleep 1
done

if [ "$i" -ge 20 ]; then
    echo "Cone started, but its private healthz did not report version 1.4.5." >&2
    journalctl -u cone -n 30 --no-pager >&2 || true
    exit 1
fi

systemctl enable nginx >/dev/null
systemctl restart nginx
health=$(curl -fsS -H 'Host: qstr.xyz' http://127.0.0.1/healthz)
printf '%s\n' "$health"
case "$health" in
    *'"version":"1.4.5"'*) ;;
    *)
        echo "nginx is running, but the proxied healthz did not report version 1.4.5." >&2
        journalctl -u nginx -n 30 --no-pager >&2 || true
        exit 1
        ;;
esac

echo "Cone 1.4.5 is running through nginx on localhost:80."
