ARG BUILDER_IMAGE=rust:1.93.1-bookworm@sha256:7c4ae649a84014c467d79319bbf17ce2632ae8b8be123ac2fb2ea5be46823f31
ARG WEB_BUILDER_IMAGE=node:24.21.0-bookworm@sha256:3d27e5c11e5786e309ec3e03f93ae536eb36e6e5eb3714d5eb3300a36157add0
ARG RUNTIME_IMAGE=debian:bookworm-20261005-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587

FROM alpine/git:v2.54.0@sha256:ce84fd6a057b4dc5cf5009da2dd832f1c6d096c1984bf50496cf3c256402264d AS source
ARG NVPN_REPO=https://github.com/btcforplebs/nostr-vpn.git
ARG NVPN_COMMIT=7cf741c971486f57a15b4ba0a496e1ef3c31e171
WORKDIR /src
# Fetch exactly one commit by SHA, then prove that is what was checked out.
# A branch name would let two installs build different code.
# The SHA check below is load-bearing: compose passes NVPN_COMMIT as a literal
# today, but a `docker build --build-arg` bypasses compose and check-pins.sh.
RUN set -e; \
    if ! printf '%s' "${NVPN_COMMIT}" | grep -Eqx '[0-9a-f]{40}'; then \
      echo "NVPN_COMMIT must be a full 40-char commit SHA, got '${NVPN_COMMIT}'" >&2; exit 1; \
    fi; \
    git init -q nostr-vpn; \
    cd nostr-vpn; \
    git fetch -q --depth 1 "${NVPN_REPO}" "${NVPN_COMMIT}"; \
    git checkout -q FETCH_HEAD; \
    test "$(git rev-parse HEAD)" = "${NVPN_COMMIT}"

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
    cargo build --release --locked -p nvpn -p nostr-vpn-web \
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
