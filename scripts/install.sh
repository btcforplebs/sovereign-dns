#!/usr/bin/env bash
# One-shot installer: writes the whole sovereign-dns stack into ./sovereign-dns,
# asks for a Pi-hole password, and brings it up. No git required.
set -euo pipefail

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker isn't installed. Get it first: https://docs.docker.com/get-docker/"
  exit 1
fi

DIR="sovereign-dns"
mkdir -p "$DIR/nvpn" "$DIR/unbound"
cd "$DIR"

cat > docker-compose.yml << 'COMPOSE'
name: sovereign-dns

services:
  nvpn:
    build:
      context: ./nvpn
      args:
        NVPN_REPO: ${NVPN_REPO:-https://github.com/btcforplebs/nostr-vpn.git}
        NVPN_REF: ${NVPN_REF:-network-dns-gui}
    container_name: sovereign-dns-nvpn
    restart: unless-stopped
    cap_add:
      - NET_ADMIN
    devices:
      - /dev/net/tun:/dev/net/tun
    volumes:
      - nvpn-config:/root/.config/nvpn
    environment:
      RUST_LOG: ${RUST_LOG:-info}
      # Only used in printed instructions, so they show the real panel URL.
      WEB_UI_PORT: ${WEB_UI_PORT:-8082}
      NVPN_ADVERTISED_ENDPOINT: ${NVPN_ADVERTISED_ENDPOINT:-}
    # WireGuard underlay. Without this published, the container's only address
    # is the Docker-internal 172.28.53.10, which no other device can reach and
    # which STUN cannot map — peers then show as present (they announce over
    # the relays) while no data path ever forms, so DNS reaches nobody.
    ports:
      - "${NVPN_LISTEN_PORT:-51820}:51820/udp"
    networks:
      dns-internal:
        ipv4_address: 172.28.53.10
    healthcheck:
      test: ["CMD-SHELL", "nvpn status >/dev/null 2>&1"]
      interval: 15s
      timeout: 10s
      retries: 5
      start_period: 20s

  # Browser control panel (nvpn-web) for approving join requests, adding
  # devices, etc. without touching a terminal — the same image nvpn builds,
  # just run with a different entrypoint/command. Reads the *same* config
  # volume as nvpn's actual daemon rather than running its own, hence
  # NVPN_EXTERNAL_DAEMON + state-file status mode. Binds 0.0.0.0 inside the
  # container (required for Docker's port publish to reach it at all) but
  # is only ever published to the host's loopback — this server has no
  # login of its own, so exposing it beyond localhost would let anyone who
  # can reach that port approve/remove devices on your mesh.
  web:
    build:
      context: ./nvpn
      args:
        NVPN_REPO: ${NVPN_REPO:-https://github.com/btcforplebs/nostr-vpn.git}
        NVPN_REF: ${NVPN_REF:-network-dns-gui}
    container_name: sovereign-dns-web
    restart: unless-stopped
    entrypoint: ["/usr/local/bin/nvpn-web"]
    command:
      - --listen
      - 0.0.0.0:8081
      - --behind-trusted-proxy
      - --config
      - /root/.config/nvpn/config.toml
    depends_on:
      nvpn:
        condition: service_healthy
    volumes:
      - nvpn-config:/root/.config/nvpn
    environment:
      NVPN_CLI_PATH: /usr/local/bin/nvpn
      NVPN_EXTERNAL_DAEMON: "true"
      NVPN_DAEMON_STATUS_MODE: state-file
      RUST_LOG: ${RUST_LOG:-info}
    ports:
      - "127.0.0.1:${WEB_UI_PORT:-8082}:8081"

  pihole:
    image: pihole/pihole:latest
    container_name: sovereign-dns-pihole
    restart: unless-stopped
    network_mode: service:nvpn
    depends_on:
      nvpn:
        condition: service_healthy
    environment:
      TZ: ${TZ:-UTC}
      FTLCONF_webserver_api_password: ${PIHOLE_PASSWORD:?set PIHOLE_PASSWORD in .env}
      FTLCONF_dns_upstreams: 172.28.53.11#53
      FTLCONF_dns_interface: ${NVPN_TUN_IFACE:-utun100}
      FTLCONF_dns_listeningMode: BIND
      FTLCONF_dns_dnssec: "true"
      FTLCONF_webserver_port: ${PIHOLE_WEB_PORT:-80}
    volumes:
      - pihole-config:/etc/pihole
    cap_add:
      - NET_BIND_SERVICE

  unbound:
    image: mvance/unbound:latest
    container_name: sovereign-dns-unbound
    restart: unless-stopped
    volumes:
      # The stock image forwards everything to Cloudflare over DoT. Overriding
      # that file with an empty one makes Unbound actually recurse from the
      # root servers instead of being a Cloudflare client. See the file itself.
      - ./unbound/forward-records.conf:/opt/unbound/etc/unbound/forward-records.conf:ro
    networks:
      dns-internal:
        ipv4_address: 172.28.53.11
    healthcheck:
      test: ["CMD-SHELL", "drill @127.0.0.1 cloudflare.com >/dev/null 2>&1 || dig @127.0.0.1 cloudflare.com >/dev/null 2>&1"]
      interval: 30s
      timeout: 10s
      retries: 5

networks:
  dns-internal:
    driver: bridge
    internal: false
    ipam:
      config:
        - subnet: 172.28.53.0/24

volumes:
  nvpn-config:
  pihole-config:
COMPOSE

cat > unbound/forward-records.conf << 'FORWARDRECORDS'
# Deliberately empty: this stack recurses, it does not forward.
#
# The stock mvance/unbound image ships this file with an active
#
#     forward-zone:
#       name: "."
#       forward-tls-upstream: yes
#       forward-addr: 1.1.1.1@853#cloudflare-dns.com
#
# which sends every single query to Cloudflare. That is encrypted, but it
# hands a complete record of everything the network looks up to a third
# party -- the exact thing this stack exists to avoid. Mounting this file
# over the image's copy removes the forward zone, so Unbound resolves from
# the root servers itself using its built-in root hints.
FORWARDRECORDS

cat > nvpn/Dockerfile << 'DOCKERFILE'
ARG BUILDER_IMAGE=rust:1.93-bookworm
ARG WEB_BUILDER_IMAGE=node:24-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim

FROM alpine/git:latest AS source
ARG NVPN_REPO=https://github.com/btcforplebs/nostr-vpn.git
ARG NVPN_REF=network-dns-gui
WORKDIR /src
RUN git clone --depth 1 --branch "${NVPN_REF}" "${NVPN_REPO}" nostr-vpn

# Builds the Svelte control panel (nvpn-web's static UI) — the browser-based
# join-request approval flow, so admins never need docker exec/CLI.
FROM ${WEB_BUILDER_IMAGE} AS web-builder
WORKDIR /work
COPY --from=source /src/nostr-vpn/web/control-panel ./
RUN --mount=type=cache,target=/pnpm/store \
    corepack enable \
    && corepack prepare pnpm@10.28.2 --activate \
    && pnpm install --frozen-lockfile --store-dir /pnpm/store \
    && pnpm run build

FROM ${BUILDER_IMAGE} AS builder
RUN apt-get update \
    && apt-get install -y --no-install-recommends libclang-dev libdbus-1-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=source /src/nostr-vpn ./nostr-vpn

WORKDIR /app/nostr-vpn
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/app/nostr-vpn/target \
    cargo build --release -p nvpn -p nostr-vpn-web \
    && cp target/release/nvpn /tmp/nvpn \
    && cp target/release/nostr-vpn-web /tmp/nvpn-web

FROM ${RUNTIME_IMAGE} AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl dnsutils iproute2 iptables iputils-ping \
        libdbus-1-3 procps \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /tmp/nvpn /usr/local/bin/nvpn
COPY --from=builder /tmp/nvpn-web /usr/local/bin/nvpn-web
COPY --from=web-builder /work/dist /usr/share/nostr-vpn/web
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV RUST_LOG=info
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
DOCKERFILE

cat > nvpn/entrypoint.sh << 'ENTRYPOINT'
#!/usr/bin/env bash
# First run: create an identity. Every run: start the daemon and follow its
# log so docker logs / restart policies see the real process.
set -euo pipefail

CONFIG_DIR="${HOME}/.config/nvpn"
CONFIG_FILE="${CONFIG_DIR}/config.toml"
DAEMON_LOG="${CONFIG_DIR}/daemon.log"

FRESH_INIT=0
if [[ ! -f "$CONFIG_FILE" ]]; then
  echo "sovereign-dns: no config found, creating a fresh NostrVPN identity"
  nvpn init
  FRESH_INIT=1
fi

DEVICE_NPUB=$(awk '
  /^\[nostr\]$/ { in_nostr = 1; next }
  /^\[/ { in_nostr = 0 }
  in_nostr && /^public_key[[:space:]]*=/ { print $3 }
' "$CONFIG_FILE" | tr -d '"')

echo "sovereign-dns: this device's identity:"
echo "  npub: ${DEVICE_NPUB}"

# On a fresh install this container founds its own network (admin = itself)
# rather than requiring some other already-established admin device — that
# precondition is what made this a multi-step manual dance for existing
# users. Founding here, then immediately setting itself as the network's
# DNS node with the override on, means "docker compose up" alone produces a
# fully working sovereign DNS network with zero devices pre-added.
#
# `nvpn init` alone leaves the network disabled (default_network_enabled()
# is false) — only `set --device ...` flips it on (apply_devices_override,
# command_dispatch.rs), and that has to happen in its own call because the
# DNS-override block runs earlier in the same `set` dispatch than the
# devices-override block, so a single combined call would still see no
# active network yet.
if [[ "$FRESH_INIT" == "1" ]]; then
  echo "sovereign-dns: founding a new network on this device"
  nvpn set --device "$DEVICE_NPUB"
  echo "sovereign-dns: turning on the DNS override"
  nvpn set --network-dns-node "$DEVICE_NPUB" --network-dns-override || \
    echo "sovereign-dns: warning: could not enable the DNS override yet (will retry on next restart)"
fi

# Every start, not just the first: the endpoint the daemon advertises is what
# other devices dial. Left alone it advertises this container's Docker-internal
# address, which nothing outside the host can reach — peers then look "online"
# (they announce themselves over the relays) while no data path ever forms.
if [[ -n "${NVPN_ADVERTISED_ENDPOINT:-}" ]]; then
  echo "sovereign-dns: advertising endpoint ${NVPN_ADVERTISED_ENDPOINT}"
  nvpn set --endpoint "$NVPN_ADVERTISED_ENDPOINT" || \
    echo "sovereign-dns: warning: could not set the advertised endpoint"
fi

NETWORK_ID=$(awk '
  /^\[\[networks\]\]/ { in_net = 1; next }
  /^\[/ { in_net = 0 }
  in_net && /^network_id[[:space:]]*=/ { print $3 }
' "$CONFIG_FILE" | tr -d '"' | head -1)

if [[ -n "$NETWORK_ID" ]]; then
  echo "sovereign-dns: to add another device to this network:"
  echo "  1. On that device, run:"
  echo "       nvpn join-manual --admin-device-id ${DEVICE_NPUB} --network-id ${NETWORK_ID}"
  echo "     This only queues a join request — it does NOT add the device yet."
  echo "  2. Once you've confirmed you want that specific device on your network,"
  echo "     approve it in the browser control panel on this machine:"
  echo "       http://localhost:${WEB_UI_PORT:-8082}"
  echo "     or from the terminal (replace <joiner-npub> with its printed npub):"
  echo "       docker exec sovereign-dns-nvpn nvpn add-device --device <joiner-npub> --publish"
  echo "     Deliberately not automatic: the network ID + this device's ID are"
  echo "     shared as plain text, so auto-approving anyone who has them would"
  echo "     remove the only access control this network has."
fi

nvpn start --daemon --connect || {
  echo "sovereign-dns: daemon start failed" >&2
  exit 1
}

echo "sovereign-dns: daemon started; following ${DAEMON_LOG}"
touch "$DAEMON_LOG"
exec tail -F "$DAEMON_LOG"
ENTRYPOINT

chmod +x nvpn/entrypoint.sh

if [[ ! -f .env ]]; then
  echo
  echo "Pick a password for your Pi-hole admin dashboard (typing is hidden):"
  read -rs PIHOLE_PASSWORD
  echo
  cat > .env << ENV
PIHOLE_PASSWORD=${PIHOLE_PASSWORD}
TZ=UTC
PIHOLE_WEB_PORT=80
WEB_UI_PORT=8082
NVPN_REPO=https://github.com/btcforplebs/nostr-vpn.git
NVPN_REF=network-dns-gui
NVPN_TUN_IFACE=utun100
NVPN_LISTEN_PORT=51820
# Leave empty to let the daemon discover its own address via STUN. Set to
# "<this-computer's-LAN-or-public-IP>:51820" if your devices can't connect.
NVPN_ADVERTISED_ENDPOINT=
RUST_LOG=info
ENV
fi

WEB_UI_PORT=$(grep '^WEB_UI_PORT=' .env | cut -d= -f2 || true)
WEB_UI_PORT=${WEB_UI_PORT:-8082}

echo
echo "Building and starting (first run compiles from source, ~10-15 minutes)..."
docker compose up -d --build

echo
echo "Waiting for the NostrVPN identity and network to be ready..."
for _ in $(seq 1 30); do
  NPUB_LINE=$(docker compose logs nvpn 2>/dev/null | grep "npub:" || true)
  INVITE_LINE=$(docker compose logs nvpn 2>/dev/null | grep "nvpn join-manual" || true)
  if [[ -n "$NPUB_LINE" && -n "$INVITE_LINE" ]]; then
    break
  fi
  sleep 2
done

echo
echo "=========================================="
echo "Done! This container founded its own NostrVPN network and turned on"
echo "the DNS override for itself — nothing else required."
echo
if [[ -n "${NPUB_LINE:-}" ]]; then
  echo "This device's ID (safe to share): ${NPUB_LINE#*npub: }"
fi
echo
echo "Your VPN control panel (manage devices, approve join requests):"
echo "  http://localhost:${WEB_UI_PORT}"
echo "It's only reachable from this computer — it has no login of its own,"
echo "so it is deliberately never exposed to the network."
echo
if [[ -n "${INVITE_LINE:-}" ]]; then
  echo "To add a phone/laptop to this network:"
  echo "  1. On that device, run:"
  echo "       $(echo "$INVITE_LINE" | sed 's/^.*| *//')"
  echo "     (this only queues a join request — it does not add the device yet)"
  echo "  2. Approve the request in the control panel above, or from the terminal:"
  echo "       docker exec sovereign-dns-nvpn nvpn add-device --device <joiner-npub> --publish"
else
  echo "To get the invite + approval commands for adding other devices, run:"
  echo "  docker compose logs nvpn | grep -A8 'to add another device'"
fi
echo
echo "Manage ad-blocking at: http://<this-computer's-mesh-ip>/admin"
echo "=========================================="
