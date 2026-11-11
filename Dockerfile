FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

ARG TARGETOS TARGETARCH VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o /out/ndb ./cmd/ndb

# Empty dirs, copied below with the nonroot owner (65532), so that named
# volumes start writable.
RUN mkdir -p /out/data /out/run

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/ndb /usr/local/bin/ndb
COPY --from=build --chown=65532:65532 /out/data /var/lib/ndb
COPY --from=build --chown=65532:65532 /out/run /run/ndb

VOLUME /var/lib/ndb /run/ndb

EXPOSE 7070

# The daemon answers /_health on its socket. A daemon that is alive but does
# not answer (it hangs) makes the container "unhealthy"; without this check
# Docker shows it as "Up". With only a TCP listener, set NDB_URL.
HEALTHCHECK --interval=5s --timeout=2s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/ndb", "health"]

ENTRYPOINT ["/usr/local/bin/ndb"]
CMD ["serve"]
