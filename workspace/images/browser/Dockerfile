# The per-project browser (internal/adapters/browser). Build context: workspace/.
#
# node, @playwright/mcp and the Chromium its Playwright drives, plus the shim —
# built from the same source as the workspace's — whose `browser-session` runs
# one MCP server per agent session. The browser has no network but the
# engine's routing proxy, so everything is installed here, at build time; it
# idles on `sleep` until a session is exec'd into it. The browser comes from
# Playwright's own CLI — the copy @playwright/mcp depends on — so its build is
# the one the server expects.
FROM golang:1.26-alpine AS shim
ARG SHIM_HASH=dev
COPY shim/ /src/
RUN cd /src && go mod init apothecary-shim >/dev/null \
    && go get github.com/itchyny/gojq/cli@v0.12.19 \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.sourceHash=${SHIM_HASH}" -o /apothecary-shim .
FROM node:22-bookworm-slim
ENV PLAYWRIGHT_BROWSERS_PATH=/ms-playwright NODE_ENV=production
RUN npm install -g @playwright/mcp@0.0.83 \
    && node /usr/local/lib/node_modules/@playwright/mcp/node_modules/playwright/cli.js install --with-deps chromium \
    && npm cache clean --force && rm -rf /var/lib/apt/lists/*
COPY --from=shim /apothecary-shim /usr/local/bin/apothecary-shim
CMD ["sleep", "infinity"]
