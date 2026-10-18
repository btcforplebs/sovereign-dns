ARG BUILDER_IMAGE=golang:1.26.9-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade

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
