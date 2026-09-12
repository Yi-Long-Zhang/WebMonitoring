FROM mcr.microsoft.com/playwright:v1.63.0-noble
WORKDIR /app
COPY package.json pnpm-workspace.yaml pnpm-lock.yaml ./
COPY apps/browser-worker/package.json apps/browser-worker/tsconfig.json apps/browser-worker/
RUN corepack enable && pnpm install --filter @webmonitoring/browser-worker... --frozen-lockfile
COPY apps/browser-worker/src apps/browser-worker/src
RUN pnpm --filter @webmonitoring/browser-worker build
ENV NODE_ENV=production
USER pwuser
EXPOSE 8081
CMD ["node", "apps/browser-worker/dist/index.js"]
