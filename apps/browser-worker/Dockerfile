FROM mcr.microsoft.com/playwright:v1.63.0-noble
WORKDIR /app
COPY apps/browser-worker/package.json apps/browser-worker/tsconfig.json ./
RUN corepack enable && pnpm install --frozen-lockfile=false
COPY apps/browser-worker/src ./src
RUN pnpm build
ENV NODE_ENV=production
USER pwuser
EXPOSE 8081
CMD ["node", "dist/index.js"]
