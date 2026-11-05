# Build context: the repo root (see docker-compose.yml). The layout of the repo
# is kept, so the "replace" in go.mod points to the SDK.
FROM golang:1.26-alpine AS build

WORKDIR /repo

COPY sdk/go ./sdk/go
COPY deploy/examples/single-vm/apps/go ./deploy/examples/single-vm/apps/go

WORKDIR /repo/deploy/examples/single-vm/apps/go

RUN CGO_ENABLED=0 go build -trimpath -o /out/visits .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/visits /visits

ENTRYPOINT ["/visits"]
