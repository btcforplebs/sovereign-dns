# shellcheck shell=bash
# This box's address on the home network: the one it would use to reach the
# internet, which is the LAN address behind a home router. Shared by
# scripts/hub-lan.sh and scripts/dns-lan.sh. Sourced; defines lan_ip only.
lan_ip() {
  local ip=""
  if command -v ip >/dev/null 2>&1; then
    ip=$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n1)
  elif command -v route >/dev/null 2>&1 && command -v ipconfig >/dev/null 2>&1; then
    local ifc
    ifc=$(route -n get default 2>/dev/null | awk '/interface:/ { print $2 }')
    [[ -n "$ifc" ]] && ip=$(ipconfig getifaddr "$ifc" 2>/dev/null || true)
  fi
  echo "$ip"
}
