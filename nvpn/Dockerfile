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
