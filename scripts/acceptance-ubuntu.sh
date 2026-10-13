#!/bin/bash
# Run only on a disposable VM. This intentionally installs a real system unit.
set -Eeuo pipefail
umask 077

on_error() {
  local status=$1 line=$2
  printf 'Service acceptance failed at line %s.\n' "$line" >&2
  exit "$status"
}
trap 'on_error "$?" "$LINENO"' ERR

systemd_log_level_is() {
  local expected=$1 actual
  actual=$(systemctl show -p LogLevelMax --value warp-svc)
  case "$expected:$actual" in
    info:info|info:6|debug:debug|debug:7) return 0 ;;
    *) return 1 ;;
  esac
}
if [ "${RILLWAY_SERVICE_ACCEPTANCE:-}" != "1" ]; then
  echo 'Set RILLWAY_SERVICE_ACCEPTANCE=1 on a fresh Ubuntu 24.04/26.04 VM.' >&2
  exit 1
fi

. /etc/os-release
case "${ID:-}:${VERSION_ID:-}" in
  ubuntu:24.04|ubuntu:26.04) ;;
  *)
    echo "Unsupported acceptance host: ${ID:-}:${VERSION_ID:-}" >&2
    exit 1
    ;;
esac

# 1. Refuse any existing Rillway installation
if [ -e /etc/systemd/system/rillway.service ] || \
   [ -e /etc/rillway ] || \
   [ -e /var/lib/rillway ] || \
   [ -e /usr/local/lib/rillway/rillway ] || \
   [ -L /usr/local/bin/rillway ] || \
   [ -e /usr/local/bin/rillway ] || \
   [ -e /run/rillway-resource-control.sock ] || \
   [ -e /run/rillway-memory.sock ] || \
   [ -e /etc/systemd/system/rillway-resource-control.socket ] || \
   [ -e /etc/systemd/system/rillway-resource-control.service ] || \
   [ -e /etc/systemd/system/rillway-memory.socket ] || \
   [ -e /etc/systemd/system/rillway-memory.service ] || \
   [ -e /var/lib/rillway-resource-control ]; then
  echo 'Refusing to overwrite an existing Rillway installation.' >&2
  exit 1
fi

# 2. Refuse any existing REAL warp-svc.service installation before setting up dummy
if systemctl list-unit-files warp-svc.service --no-legend 2>/dev/null | grep -q 'warp-svc\.service' || \
   systemctl is-active --quiet warp-svc.service 2>/dev/null || \
   [ -e /lib/systemd/system/warp-svc.service ] || \
   [ -e /usr/lib/systemd/system/warp-svc.service ] || \
   [ -e /etc/systemd/system/warp-svc.service ]; then
  echo 'Refusing to run on a host with an existing warp-svc.service installation.' >&2
  exit 1
fi

task_binary=$(realpath "${1:-bin/rillway}")
task_temp=$(mktemp -d)
task_installed=0
dummy_warp_created=0
dummy_legacy_created=0

cleanup() {
  local exit_code=$?
  set +e

  # Clean any legacy fixture before uninstalling its simulated update target.
  if [ "$dummy_legacy_created" = 1 ]; then
    sudo systemctl disable --now rillway-memory.socket >/dev/null 2>&1 || true
    sudo systemctl stop rillway-memory.service >/dev/null 2>&1 || true
    sudo rm -f /etc/systemd/system/rillway-memory.socket /etc/systemd/system/rillway-memory.service
    sudo rm -rf /etc/systemd/system/rillway.service.d
    sudo systemctl daemon-reload >/dev/null 2>&1 || true
  fi

  # Uninstall rillway if installed
  if [ "$task_installed" = 1 ] && [ -e /etc/systemd/system/rillway.service ]; then
    sudo "$task_binary" service uninstall >/dev/null 2>&1 || true
  fi

  # Clean dummy warp service and drop-ins
  if [ "$dummy_warp_created" = 1 ]; then
    sudo systemctl stop warp-svc.service >/dev/null 2>&1 || true
    sudo systemctl disable warp-svc.service >/dev/null 2>&1 || true
    sudo rm -f /etc/systemd/system/warp-svc.service
    sudo rm -rf /etc/systemd/system/warp-svc.service.d
    sudo systemctl daemon-reload >/dev/null 2>&1 || true
  fi

  # Remove resource control state directory and socket if remaining after test
  sudo rm -rf /var/lib/rillway-resource-control
  sudo rm -f /run/rillway-resource-control.sock

  rm -rf "$task_temp"
  exit "$exit_code"
}
trap cleanup EXIT HUP INT TERM

# 3. Create dummy warp-svc.service
sudo tee /etc/systemd/system/warp-svc.service > /dev/null <<'EOF'
[Unit]
Description=Dummy Cloudflare WARP service for Rillway acceptance
After=network.target

[Service]
Type=simple
ExecStart=/bin/sleep infinity
Restart=always

[Install]
WantedBy=multi-user.target
EOF
dummy_warp_created=1
sudo systemctl daemon-reload
sudo systemctl start warp-svc.service
sudo systemctl is-active --quiet warp-svc.service

# 4. Install Rillway on a clean host before simulating an upgraded legacy installation.
task_installed=1
"$task_binary" setup --yes --config "$task_temp/config.json"

# Verify base service installation.
sudo systemctl is-active --quiet rillway
test "$(systemctl show -p User --value rillway)" = rillway
test "$(readlink /usr/local/bin/rillway)" = /usr/local/lib/rillway/rillway
sudo test -f /etc/rillway/config.json
test "$(sudo stat -c %a /etc/rillway)" = 700
test "$(sudo stat -c %a /var/lib/rillway)" = 700
test "$(sudo stat -c %a /etc/rillway/config.json)" = 600

# 5. Simulate the legacy memory control left by the previous release, then
# reconcile it through the root restart path used for an in-place upgrade.
sudo mkdir -p /var/lib/rillway-resource-control
printf '[Service]\nMemoryMax=512M\n' | sudo tee /var/lib/rillway-resource-control/memory-limit.conf > /dev/null
sudo chmod 0600 /var/lib/rillway-resource-control/memory-limit.conf
sudo chown -R root:root /var/lib/rillway-resource-control

sudo tee /etc/systemd/system/rillway-memory.socket > /dev/null <<'EOF'
[Unit]
Description=Legacy Rillway Memory Limit Socket (Simulation)
[Socket]
ListenStream=/run/rillway-memory.sock
SocketMode=0660
SocketUser=root
SocketGroup=root
[Install]
WantedBy=sockets.target
EOF

sudo tee /etc/systemd/system/rillway-memory.service > /dev/null <<'EOF'
[Unit]
Description=Legacy Rillway Memory Limit Service (Simulation)
[Service]
Type=oneshot
ExecStart=/bin/true
EOF
dummy_legacy_created=1
sudo systemctl daemon-reload
sudo systemctl enable --now rillway-memory.socket
sudo "$task_binary" service restart --config /etc/rillway/config.json

# 6. Verify legacy memory migration and resource-control socket.
sudo systemctl is-active --quiet rillway-resource-control.socket
test -S /run/rillway-resource-control.sock

# Old legacy units must be stopped/disabled/removed
if systemctl is-active --quiet rillway-memory.socket 2>/dev/null; then
  echo 'Legacy rillway-memory.socket is still active after migration' >&2
  exit 1
fi
if [ -e /etc/systemd/system/rillway-memory.socket ] || [ -e /etc/systemd/system/rillway-memory.service ]; then
  echo 'Legacy rillway-memory unit files were not removed during migration' >&2
  exit 1
fi

# Existing memory limit configuration must be preserved
test "$(sudo cat /var/lib/rillway-resource-control/memory-limit.conf)" = "$(printf '[Service]\nMemoryMax=512M')"
sudo test -L /etc/systemd/system/rillway.service.d/zzzz-rillway-memory.conf
test "$(sudo readlink -f /etc/systemd/system/rillway.service.d/zzzz-rillway-memory.conf)" = /var/lib/rillway-resource-control/memory-limit.conf

# 7. Verify WARP service logging reconciliation
# Managed state file must exist with default info level
sudo test -f /var/lib/rillway-resource-control/warp-log-level.conf
test "$(sudo cat /var/lib/rillway-resource-control/warp-log-level.conf)" = "$(printf '[Service]\nLogLevelMax=info')"

# Systemd drop-in must be a symlink pointing to the managed state file
sudo test -L /etc/systemd/system/warp-svc.service.d/zzzz-rillway-log-level.conf
test "$(sudo readlink -f /etc/systemd/system/warp-svc.service.d/zzzz-rillway-log-level.conf)" = /var/lib/rillway-resource-control/warp-log-level.conf

# Effective LogLevelMax of warp-svc must be info
systemd_log_level_is info

# 8. Wait for Rillway control and proxy services
task_attempt=0
until curl --max-time 5 --fail --silent http://127.0.0.1:17893/proxy.pac > "$task_temp/proxy.pac"; do
  task_attempt=$((task_attempt + 1))
  if [ "$task_attempt" -ge 15 ]; then
    echo 'Timed out waiting for proxy service' >&2
    exit 1
  fi
  sleep 1
done

sudo cat /var/lib/rillway/admin.crt > "$task_temp/admin.crt"
sudo cat /var/lib/rillway/admin.token > "$task_temp/token"

curl_api() {
  local method="$1"
  local endpoint="$2"
  local data="${3:-}"
  if [ -n "$data" ]; then
    curl --max-time 5 --silent --show-error \
      --cacert "$task_temp/admin.crt" \
      -H "Authorization: Bearer $(tr -d '\n\r' < "$task_temp/token")" \
      -H "Content-Type: application/json" \
      -X "$method" \
      --data "$data" \
      -w "\n%{http_code}" \
      "https://127.0.0.1:17892$endpoint"
  else
    curl --max-time 5 --silent --show-error \
      --cacert "$task_temp/admin.crt" \
      -H "Authorization: Bearer $(tr -d '\n\r' < "$task_temp/token")" \
      -X "$method" \
      -w "\n%{http_code}" \
      "https://127.0.0.1:17892$endpoint"
  fi
}

# 9. Verify Outbounds forced refresh query parameter validation
# Normal query (refresh=false or absent) returns 200
outbounds_resp=$(curl_api GET "/api/v1/outbounds?refresh=false")
outbounds_code=$(echo "$outbounds_resp" | tail -n 1)
test "$outbounds_code" = "200"

# Forced refresh (refresh=true) returns 200
outbounds_force_resp=$(curl_api GET "/api/v1/outbounds?refresh=true")
outbounds_force_code=$(echo "$outbounds_force_resp" | tail -n 1)
test "$outbounds_force_code" = "200"

# Invalid refresh query value returns 400
bad_refresh_resp=$(curl_api GET "/api/v1/outbounds?refresh=invalid_value")
bad_refresh_code=$(echo "$bad_refresh_resp" | tail -n 1)
test "$bad_refresh_code" = "400"

# 10. Verify WARP logging API: GET status
warp_status_resp=$(curl_api GET "/api/v1/service/logging/warp")
warp_status_code=$(echo "$warp_status_resp" | tail -n 1)
warp_status_body=$(echo "$warp_status_resp" | sed '$d')
test "$warp_status_code" = "200"

# Must be supported with level info
echo "$warp_status_body" | grep -q '"supported":true'
echo "$warp_status_body" | grep -q '"level":"info"'
echo "$warp_status_body" | grep -q '"effective_level":"info"'

# Extract initial revision
initial_revision=$(echo "$warp_status_body" | grep -o '"revision":"[^"]*"' | cut -d'"' -f4)
test -n "$initial_revision"

# 11. Verify CAS conflict (409) with stale revision
stale_resp=$(curl_api PUT "/api/v1/service/logging/warp" '{"level":"debug","revision":"stale_revision_value"}')
stale_code=$(echo "$stale_resp" | tail -n 1)
test "$stale_code" = "409"
# Effective level must remain unchanged
systemd_log_level_is info

# 12. Verify invalid level validation (422)
invalid_level_resp=$(curl_api PUT "/api/v1/service/logging/warp" "{\"level\":\"unsupported_level\",\"revision\":\"$initial_revision\"}")
invalid_level_code=$(echo "$invalid_level_resp" | tail -n 1)
test "$invalid_level_code" = "422"

# 13. Verify valid update to debug, systemd LogLevelMax update, and warp-svc restart
warp_pid_before=$(systemctl show -p MainPID --value warp-svc)

debug_resp=$(curl_api PUT "/api/v1/service/logging/warp" "{\"level\":\"debug\",\"revision\":\"$initial_revision\"}")
debug_code=$(echo "$debug_resp" | tail -n 1)
debug_body=$(echo "$debug_resp" | sed '$d')
test "$debug_code" = "200"
echo "$debug_body" | grep -q '"level":"debug"'
echo "$debug_body" | grep -q '"effective_level":"debug"'

debug_revision=$(echo "$debug_body" | grep -o '"revision":"[^"]*"' | cut -d'"' -f4)
test -n "$debug_revision"
test "$debug_revision" != "$initial_revision"

# Verify systemd state and effective LogLevelMax
test "$(sudo cat /var/lib/rillway-resource-control/warp-log-level.conf)" = "$(printf '# rillway-resource-control\n[Service]\nLogLevelMax=debug')"
systemd_log_level_is debug

# Verify that warp-svc was restarted (Main PID changed)
warp_pid_after=$(systemctl show -p MainPID --value warp-svc)
test "$warp_pid_before" != "$warp_pid_after"

# 14. Verify restart failure rollback behavior
# Temporarily break warp-svc.service unit so restarting it fails
sudo sed -i 's|ExecStart=/bin/sleep infinity|ExecStart=/bin/false|' /etc/systemd/system/warp-svc.service
sudo systemctl daemon-reload

# Attempt to change level to warning - restart will fail and helper must roll back to debug
rollback_resp=$(curl_api PUT "/api/v1/service/logging/warp" "{\"level\":\"warning\",\"revision\":\"$debug_revision\"}")
rollback_code=$(echo "$rollback_resp" | tail -n 1)
# Should return error (500 or non-200)
test "$rollback_code" != "200"

# Managed state file must have been restored to debug (NOT warning)
test "$(sudo cat /var/lib/rillway-resource-control/warp-log-level.conf)" = "$(printf '# rillway-resource-control\n[Service]\nLogLevelMax=debug')"

# Restore warp-svc.service unit
sudo sed -i 's|ExecStart=/bin/false|ExecStart=/bin/sleep infinity|' /etc/systemd/system/warp-svc.service
sudo systemctl daemon-reload
sudo systemctl restart warp-svc.service

# 15. Verify update to inherit (clears LogLevelMax assignment)
inherit_resp=$(curl_api PUT "/api/v1/service/logging/warp" "{\"level\":\"inherit\",\"revision\":\"$debug_revision\"}")
inherit_code=$(echo "$inherit_resp" | tail -n 1)
inherit_body=$(echo "$inherit_resp" | sed '$d')
test "$inherit_code" = "200"
echo "$inherit_body" | grep -q '"level":"inherit"'

# Managed state file must contain empty LogLevelMax=
test "$(sudo cat /var/lib/rillway-resource-control/warp-log-level.conf)" = "$(printf '# rillway-resource-control\n[Service]\nLogLevelMax=')"
inherit_effective=$(systemctl show -p LogLevelMax --value warp-svc)
test "$inherit_effective" = "" || test "$inherit_effective" = "-1"

# 16. Verify Root CLI service restart reconciles and restarts
sudo "$task_binary" service restart --config /etc/rillway/config.json
sudo systemctl is-active --quiet rillway
sudo systemctl is-active --quiet rillway-resource-control.socket
test -S /run/rillway-resource-control.sock

# 17. Verify service uninstall cleans units and symlinks while preserving state directory
sudo "$task_binary" service uninstall
if systemctl is-active --quiet rillway 2>/dev/null; then
  echo 'rillway service is still active after uninstall' >&2
  exit 1
fi
if [ -e /etc/systemd/system/rillway-resource-control.socket ] || [ -e /etc/systemd/system/rillway-resource-control.service ]; then
  echo 'rillway-resource-control unit files were not removed during uninstall' >&2
  exit 1
fi
if [ -e /etc/systemd/system/warp-svc.service.d/zzzz-rillway-log-level.conf ]; then
  echo 'WARP drop-in symlink was not removed during uninstall' >&2
  exit 1
fi
# State directory is preserved for rollback/reinstallation
sudo test -f /var/lib/rillway-resource-control/memory-limit.conf
sudo test -f /var/lib/rillway-resource-control/warp-log-level.conf

printf 'Service acceptance passed on Ubuntu %s\n' "$VERSION_ID"
