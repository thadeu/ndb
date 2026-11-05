# Build context: the repo root (see docker-compose.yml). The SDK is built and
# packed here, then installed like a normal package.
FROM node:24-slim AS sdk

RUN corepack enable

WORKDIR /sdk

COPY sdk/ts ./

RUN pnpm install --frozen-lockfile && pnpm build && pnpm pack --out /ndb.tgz

FROM node:24-slim

WORKDIR /app

COPY --from=sdk /ndb.tgz /ndb.tgz
COPY deploy/examples/single-vm/apps/node/package.json deploy/examples/single-vm/apps/node/app.mjs ./

RUN npm install --omit=dev /ndb.tgz

CMD ["node", "app.mjs"]
