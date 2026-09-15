FROM mcr.microsoft.com/playwright:v1.63.0-noble
WORKDIR /app
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY apps/browser-worker/package.json apps/browser-worker/tsconfig.json apps/browser-worker/
RUN --mount=type=cache,id=webmonitoring-corepack,target=/root/.cache/node/corepack,sharing=locked \
    --mount=type=cache,id=webmonitoring-pnpm-store,target=/root/.local/share/pnpm/store/v11,sharing=locked \
    corepack enable && pnpm install --filter @webmonitoring/browser-worker... --frozen-lockfile
COPY apps/browser-worker/src apps/browser-worker/src
# Reusing the corepack cache keeps the pnpm version pinned by the install step
# instead of re-downloading it from the registry on every build.
RUN --mount=type=cache,id=webmonitoring-corepack,target=/root/.cache/node/corepack,sharing=locked \
    pnpm --filter @webmonitoring/browser-worker build
ENV NODE_ENV=production
USER pwuser
EXPOSE 8081
CMD ["node", "apps/browser-worker/dist/index.js"]
