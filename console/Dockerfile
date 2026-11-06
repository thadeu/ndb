FROM --platform=$BUILDPLATFORM oven/bun:1.4.4 AS build

ARG TARGETARCH

WORKDIR /src

COPY package.json bun.lock ./
RUN bun install --frozen-lockfile

COPY tsconfig.json ./
COPY src ./src

RUN bun run client && \
    target=$([ "$TARGETARCH" = "arm64" ] && echo bun-linux-arm64 || echo bun-linux-x64) && \
    bun build --compile --minify --target=$target src/main.ts --outfile /out/ndb-ui

FROM gcr.io/distroless/cc-debian12:nonroot

ENV PORT=4321 HOST=0.0.0.0

COPY --from=build /out/ndb-ui /usr/local/bin/ndb-ui

EXPOSE 4321

ENTRYPOINT ["/usr/local/bin/ndb-ui"]
