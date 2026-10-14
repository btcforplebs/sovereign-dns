ARG BUILDER_IMAGE=golang:1.23.12-alpine@sha256:383395b794dffa5b53012a212365d40c8e37109a626ca30d6151c8348d380b5f

FROM ${BUILDER_IMAGE} AS builder
WORKDIR /src
COPY go.mod go.sum ./
COPY *.go page.html ./
# go build checks every module against go.sum and fails on any mismatch.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /settings-sync .

# Scratch, like the hub: no shell or package manager behind a web page. The CA
# bundle comes from the digest-pinned builder, for wss:// relays.
FROM scratch AS runtime
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /settings-sync /settings-sync
EXPOSE 8083
ENTRYPOINT ["/settings-sync"]
