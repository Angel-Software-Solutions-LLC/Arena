# syntax=docker/dockerfile:1
#
# Sidecar that holds Docker-socket (host-root-equivalent) access so the
# internet/tailnet-facing orchestrator process never has to. See
# updater/server.mjs for the security model. Built from a separate context
# (updater/) so it never inherits the main app's dependency tree.

FROM docker:29-cli AS docker-cli

FROM node:26-bookworm-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends rsync tar ca-certificates git openssh-client \
  && rm -rf /var/lib/apt/lists/*

COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=docker-cli /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose

WORKDIR /app
# server.mjs imports ./compose.mjs at runtime -- both must be present in the
# image, or the sidecar crash-loops on startup with ERR_MODULE_NOT_FOUND.
COPY server.mjs compose.mjs manifest.mjs demobots.mjs ./

EXPOSE 8090
CMD ["node", "server.mjs"]
