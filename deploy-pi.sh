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

systemctl stop cone
install -m 0755 "$source_binary" "$repo_dir/cone"
systemctl start cone

i=0
while [ "$i" -lt 20 ]; do
    if health=$(curl -fsS http://127.0.0.1:8080/healthz 2>/dev/null); then
        printf '%s\n' "$health"
        case "$health" in
            *'"version":"1.4.0"'*) exit 0 ;;
        esac
    fi
    i=$((i + 1))
    sleep 1
done

echo "Cone started, but healthz did not report version 1.4.0." >&2
journalctl -u cone -n 30 --no-pager >&2 || true
exit 1
