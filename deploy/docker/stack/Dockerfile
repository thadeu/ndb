# ndb-stack: the daemon and the console in one image, for development.
# It is built FROM the published images, so a new version needs no edit
# here: CI passes NDB_TAG, and a local build can point NDB_IMAGE and
# NDB_UI_IMAGE at local images.

ARG NDB_TAG=latest
ARG NDB_IMAGE=ghcr.io/thadeu/ndb
ARG NDB_UI_IMAGE=ghcr.io/thadeu/ndb-ui

FROM ${NDB_IMAGE}:${NDB_TAG} AS ndb

FROM ${NDB_UI_IMAGE}:${NDB_TAG} AS ndb-ui

# The cc base of ndb-ui (glibc for the Bun binary), with busybox for the
# entrypoint script.
FROM gcr.io/distroless/cc-debian12:debug-nonroot

LABEL org.opencontainers.image.title="ndb-stack" \
      org.opencontainers.image.description="ndb and its web console in one image, for development" \
      org.opencontainers.image.source="https://github.com/thadeu/ndb" \
      org.opencontainers.image.licenses="AGPL-3.0"

ENV PORT=4321 HOST=0.0.0.0

COPY --from=ndb /usr/local/bin/ndb /usr/local/bin/ndb
COPY --from=ndb-ui /usr/local/bin/ndb-ui /usr/local/bin/ndb-ui
COPY --from=ndb --chown=65532:65532 /var/lib/ndb /var/lib/ndb
COPY --from=ndb --chown=65532:65532 /run/ndb /run/ndb
COPY --chmod=0755 entrypoint.sh /usr/local/bin/ndb-stack

VOLUME /var/lib/ndb

EXPOSE 7070 4321

HEALTHCHECK --interval=5s --timeout=2s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/ndb", "health"]

ENTRYPOINT ["/usr/local/bin/ndb-stack"]
