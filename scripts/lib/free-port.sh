# shellcheck shell=bash
# Pick the UDP port the box's VPN listens on.
#
# Sourced by install.sh; defines functions and does nothing on its own.
#
#   fp_udp_port_free PORT            0 = free, 1 = taken, 2 = could not tell
#   fp_pick_free_udp_port FROM [N]   prints the first free port in FROM..FROM+N-1
#   nvpn_listen_port [ENV_FILE]      prints the port to use for NVPN_LISTEN_PORT
#                                    and saves it in ENV_FILE (default .env)
#
# Why: the box publishes UDP 51820. When the Nostr VPN app also runs on the
# same computer it already holds 51820, and the container fails to start.
#
# How a port is judged: we try to bind it ourselves, on 0.0.0.0 and on [::],
# which is what Docker does when it publishes the port. That needs no root and
# sees sockets owned by root (the VPN helper runs as root), unlike lsof, and
# unlike netstat, which lists no inet sockets at all on macOS 27. The probe is
# the system perl, which ships with macOS and nearly every Linux. Without
# perl, Linux falls back to reading /proc/net/udp and /proc/net/udp6.
#
# FP_PROBE=perl|proc forces one probe (tests use this).

FP_DEFAULT_PORT=51820

fp_log() { printf '%s\n' "$*" >&2; }

# 0 free, 1 taken, 2 probe failed.
fp_probe_perl() {
  command -v perl >/dev/null 2>&1 || return 2
  perl -MSocket=:all -e '
    my $port = $ARGV[0];
    socket(my $v4, PF_INET, SOCK_DGRAM, 0) or exit 2;
    bind($v4, pack_sockaddr_in($port, INADDR_ANY)) or exit 1;
    # A host without IPv6 cannot open the socket; that is not a clash.
    if (socket(my $v6, AF_INET6, SOCK_DGRAM, 0)) {
      setsockopt($v6, IPPROTO_IPV6, IPV6_V6ONLY, 1);
      bind($v6, pack_sockaddr_in6($port, IN6ADDR_ANY)) or exit 1;
    }
    exit 0;
  ' "$1" 2>/dev/null
}

fp_probe_proc() {
  [[ -r /proc/net/udp ]] || return 2
  local hex
  hex=$(printf '%04X' "$1")
  # local_address is "ADDR:PORT" with the port in upper-case hex.
  if awk -v p=":$hex" 'NR > 1 && substr($2, length($2) - 4) == p { found = 1 } END { exit !found }' \
      /proc/net/udp /proc/net/udp6 2>/dev/null; then
    return 1
  fi
  return 0
}

fp_udp_port_free() {
  local port=$1 rc
  case "${FP_PROBE:-}" in
    perl) fp_probe_perl "$port"; return ;;
    proc) fp_probe_proc "$port"; return ;;
  esac
  fp_probe_perl "$port"; rc=$?
  [[ $rc == 2 ]] || return "$rc"
  fp_probe_proc "$port"
}

fp_pick_free_udp_port() {
  local from=$1 count=${2:-50} port rc
  for ((port = from; port < from + count && port <= 65535; port++)); do
    fp_udp_port_free "$port"; rc=$?
    if [[ $rc == 0 ]]; then
      printf '%s\n' "$port"
      return 0
    fi
    if [[ $rc == 2 ]]; then
      fp_log "Can't check which UDP ports are free here (no perl, no /proc); assuming $port is free."
      printf '%s\n' "$port"
      return 0
    fi
  done
  fp_log "No free UDP port between $from and $((from + count - 1))."
  return 1
}

# The host UDP port an existing sovereign-dns-nvpn container publishes,
# running or stopped. A reinstall must keep that port, not move away from
# itself. Prints the port and returns 0; returns 1 when there is no such
# container; returns 2 when Docker can't be read, so we can't tell.
fp_existing_box_port() {
  command -v docker >/dev/null 2>&1 || return 1
  local out
  if ! out=$(docker inspect sovereign-dns-nvpn --format '{{json .HostConfig.PortBindings}}' 2>&1); then
    case "$out" in
      *"No such object"* | *"no such object"*) return 1 ;;
      *) return 2 ;;
    esac
  fi
  # e.g. {"51820/udp":[{"HostIp":"","HostPort":"51820"}]}
  out=$(printf '%s\n' "$out" | sed -n 's/.*"51820\/udp":\[{[^]]*"HostPort":"\([0-9][0-9]*\)".*/\1/p')
  [[ -n "$out" ]] || return 2
  printf '%s\n' "$out"
}

# Prints the port and records it in ENV_FILE so later runs keep it. Call it
# after install.sh has written .env: it adds a line, and refuses to create
# the file (install.sh only writes .env when there is none).
nvpn_listen_port() {
  local env_file=${1:-.env} port chosen rc

  if [[ ! -f "$env_file" ]]; then
    fp_log "nvpn_listen_port: $env_file doesn't exist yet; write it first."
    return 1
  fi

  # 1. Already chosen on an earlier run: keep it.
  port=$(sed -n 's/^NVPN_LISTEN_PORT=\([0-9][0-9]*\)[[:space:]]*$/\1/p' "$env_file" | tail -n 1)
  if [[ -n "$port" ]]; then
    fp_log "VPN port: UDP $port (kept from $env_file)."
    printf '%s\n' "$port"
    return 0
  fi

  # 2. This box already exists (running or stopped): keep its port.
  port=$(fp_existing_box_port); rc=$?
  if [[ $rc == 2 ]]; then
    # Probing now would find the box's own port taken and move it.
    fp_log "Can't read Docker to check whether this box already has a VPN port. Make sure Docker is running, then run this again."
    return 1
  fi
  if [[ $rc == 0 ]]; then
    fp_log "VPN port: UDP $port (already used by this box)."
    chosen=$port
  else
    # 3. Fresh install: 51820 unless something else on this computer has it.
    chosen=$(fp_pick_free_udp_port "$FP_DEFAULT_PORT") || return 1
    if [[ "$chosen" == "$FP_DEFAULT_PORT" ]]; then
      fp_log "VPN port: UDP $chosen."
    else
      fp_log "VPN port: UDP $chosen. UDP $FP_DEFAULT_PORT is already in use on this computer (often the Nostr VPN app), so the box uses $chosen instead."
    fi
  fi

  # Don't glue the new line onto a last line that has no newline.
  if [[ -s "$env_file" && -n "$(tail -c 1 "$env_file")" ]]; then
    printf '\n' >> "$env_file" || return 1
  fi
  printf 'NVPN_LISTEN_PORT=%s\n' "$chosen" >> "$env_file" || return 1
  printf '%s\n' "$chosen"
}
