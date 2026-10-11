ARG BUILDER_IMAGE=golang:1.23.12-alpine@sha256:383395b794dffa5b53012a212365d40c8e37109a626ca30d6151c8348d380b5f

FROM ${BUILDER_IMAGE} AS builder
WORKDIR /src
COPY go.mod ./
COPY *.go ./
# Static build so the runtime image can be scratch: this container fronts an
# unauthenticated admin surface, so it ships no shell, no package manager and
# no libc for anything that reaches it to work with.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /hub .

FROM scratch AS runtime
COPY --from=builder /hub /hub
EXPOSE 8080
ENTRYPOINT ["/hub"]
