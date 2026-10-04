#!/bin/sh
# Run only on a disposable VM. This intentionally installs a real system unit.
set -eu
umask 077
if [ "${RILLWAY_SERVICE_ACCEPTANCE:-}" != "1" ]; then
  echo 'Set RILLWAY_SERVICE_ACCEPTANCE=1 on a fresh Ubuntu 24.04/26.04 VM.' >&2
  exit 1
fi
. /etc/os-release
case "$ID:$VERSION_ID" in ubuntu:24.04|ubuntu:26.04) ;; *) echo 'Unsupported acceptance host' >&2; exit 1;; esac
if [ -e /etc/systemd/system/rillway.service ] || [ -e /etc/rillway ] || [ -e /var/lib/rillway ] || [ -e /usr/local/lib/rillway/rillway ] || [ -L /usr/local/bin/rillway ] || [ -e /usr/local/bin/rillway ]; then
  echo 'Refusing to overwrite an existing Rillway installation.' >&2
  exit 1
fi
task_binary=$(realpath "${1:-bin/rillway}")
task_temp=$(mktemp -d)
task_installed=0
cleanup() {
  if [ "$task_installed" = 1 ] && [ -e /etc/systemd/system/rillway.service ]; then sudo "$task_binary" service uninstall; fi
  rm -rf "$task_temp"
}
trap cleanup EXIT HUP INT TERM
task_installed=1
"$task_binary" setup --yes --config "$task_temp/config.json"
sudo systemctl is-active --quiet rillway
test "$(systemctl show -p User --value rillway)" = rillway
test "$(readlink /usr/local/bin/rillway)" = /usr/local/lib/rillway/rillway
sudo test -f /etc/rillway/config.json
test "$(sudo stat -c %a /etc/rillway)" = 700
test "$(sudo stat -c %a /var/lib/rillway)" = 700
test "$(sudo stat -c %a /etc/rillway/config.json)" = 600
task_attempt=0
until curl --max-time 5 --fail --silent http://127.0.0.1:17893/proxy.pac > "$task_temp/proxy.pac"; do
  task_attempt=$((task_attempt + 1))
  if [ "$task_attempt" -ge 15 ]; then exit 1; fi
  sleep 1
done
sudo cat /var/lib/rillway/admin.crt > "$task_temp/admin.crt"
sudo cat /var/lib/rillway/admin.token > "$task_temp/token"
{ printf 'header = "Authorization: Bearer '; tr -d '\n\r' < "$task_temp/token"; printf '"\n'; } |
  curl --max-time 5 --fail --silent --cacert "$task_temp/admin.crt" --config - https://127.0.0.1:17892/api/v1/config > "$task_temp/active.json"
curl --max-time 5 --fail --silent --noproxy '' --proxy http://127.0.0.1:17890 http://127.0.0.1:17893/proxy.pac > "$task_temp/proxied.pac"
cmp "$task_temp/proxy.pac" "$task_temp/proxied.pac"
sudo "$task_binary" service stop
if systemctl is-active --quiet rillway; then echo 'Service failed to stop' >&2; exit 1; fi
sudo "$task_binary" service start
sudo systemctl is-active --quiet rillway
printf 'Service acceptance passed on Ubuntu %s\n' "$VERSION_ID"
