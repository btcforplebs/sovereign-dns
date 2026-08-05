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
