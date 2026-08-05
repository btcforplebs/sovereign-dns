#!/usr/bin/env bash
# One-shot installer: writes the whole sovereign-dns stack into ./sovereign-dns,
# asks for a Pi-hole password, and brings it up. No git required.
set -euo pipefail

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker isn't installed. Get it first: https://docs.docker.com/get-docker/"
  exit 1
fi

DIR="sovereign-dns"
mkdir -p "$DIR/nvpn"
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
    networks:
      dns-internal:
        ipv4_address: 172.28.53.10
    healthcheck:
      test: ["CMD-SHELL", "nvpn status >/dev/null 2>&1"]
      interval: 15s
      timeout: 10s
      retries: 5
      start_period: 20s

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

cat > nvpn/Dockerfile << 'DOCKERFILE'
ARG BUILDER_IMAGE=rust:1.93-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim

FROM ${BUILDER_IMAGE} AS builder
ARG NVPN_REPO=https://github.com/btcforplebs/nostr-vpn.git
ARG NVPN_REF=network-dns-gui

RUN apt-get update \
    && apt-get install -y --no-install-recommends git libclang-dev libdbus-1-dev pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
RUN git clone --depth 1 --branch "${NVPN_REF}" "${NVPN_REPO}" nostr-vpn

WORKDIR /app/nostr-vpn
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/app/nostr-vpn/target \
    cargo build --release -p nvpn \
    && cp target/release/nvpn /tmp/nvpn

FROM ${RUNTIME_IMAGE} AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl dnsutils iproute2 iptables iputils-ping \
        libdbus-1-3 procps \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /tmp/nvpn /usr/local/bin/nvpn
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV RUST_LOG=info
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
DOCKERFILE

cat > nvpn/entrypoint.sh << 'ENTRYPOINT'
#!/usr/bin/env bash
set -euo pipefail

CONFIG_DIR="${HOME}/.config/nvpn"
CONFIG_FILE="${CONFIG_DIR}/config.toml"
DAEMON_LOG="${CONFIG_DIR}/daemon.log"

if [[ ! -f "$CONFIG_FILE" ]]; then
  echo "sovereign-dns: no config found, creating a fresh NostrVPN identity"
  nvpn init
fi

echo "sovereign-dns: this device's identity:"
awk '
  /^\[nostr\]$/ { in_nostr = 1; next }
  /^\[/ { in_nostr = 0 }
  in_nostr && /^public_key[[:space:]]*=/ { print "  npub: " $3 }
' "$CONFIG_FILE" | tr -d '"'

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
NVPN_REPO=https://github.com/btcforplebs/nostr-vpn.git
NVPN_REF=network-dns-gui
NVPN_TUN_IFACE=utun100
RUST_LOG=info
ENV
fi

echo
echo "Building and starting (first run compiles from source, ~10 minutes)..."
docker compose up -d --build

echo
echo "Waiting for the NostrVPN identity to be ready..."
for _ in $(seq 1 30); do
  NPUB_LINE=$(docker compose logs nvpn 2>/dev/null | grep "npub:" || true)
  if [[ -n "$NPUB_LINE" ]]; then
    break
  fi
  sleep 2
done

echo
echo "=========================================="
echo "Done! Here's what to do next:"
echo
if [[ -n "${NPUB_LINE:-}" ]]; then
  echo "1. This device's ID (safe to share): ${NPUB_LINE#*npub: }"
else
  echo "1. Run 'docker compose logs nvpn | grep npub:' in this folder to get this device's ID."
fi
echo "2. Add that ID to your NostrVPN network like any other device."

echo
if [[ "$(uname -s)" == "Linux" ]]; then
  echo "Grabbing a matching admin CLI (no separate install needed)..."
  mkdir -p bin
  if docker cp sovereign-dns-nvpn:/usr/local/bin/nvpn bin/nvpn 2>/dev/null; then
    chmod +x bin/nvpn
    echo "3. From an admin device already joined to your network, run:"
    echo "     ./bin/nvpn set --network-dns-node <the-id-from-step-1> --network-dns-override"
    echo "   (run this from inside the sovereign-dns folder; it uses your existing"
    echo "   NostrVPN config, no reinstall needed — this is just the same binary"
    echo "   the container built, copied out so it has the DNS-override flags.)"
  else
    echo "3. Couldn't auto-copy the CLI out of the container — run this manually:"
    echo "     docker cp sovereign-dns-nvpn:/usr/local/bin/nvpn ./bin/nvpn && chmod +x ./bin/nvpn"
  fi
else
  echo "3. This machine isn't Linux (Docker's running the nvpn container in a Linux"
  echo "   VM under the hood), so the container's nvpn binary won't run here directly."
  echo "   The DNS-override flags are new — build a native CLI once on this machine:"
  echo "     curl https://sh.rustup.rs -sSf | sh -s -- -y && source \"\$HOME/.cargo/env\""
  echo "     git clone --depth 1 --branch network-dns-gui https://github.com/btcforplebs/nostr-vpn.git"
  echo "     cd nostr-vpn && cargo build --release -p nvpn"
  echo "   Then, from an admin device already joined to your network:"
  echo "     ./target/release/nvpn set --network-dns-node <the-id-from-step-1> --network-dns-override"
fi
echo "4. Manage ad-blocking at: http://<this-computer's-mesh-ip>/admin"
echo "=========================================="
