ARG BUILDER_IMAGE=golang:1.23-alpine

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
