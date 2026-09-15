FROM node:24-alpine AS build
WORKDIR /app
COPY package.json pnpm-workspace.yaml ./
COPY pnpm-lock.yaml pnpm-lock.yaml
COPY apps/console/package.json apps/console/package.json
RUN --mount=type=cache,id=webmonitoring-corepack,target=/root/.cache/node/corepack,sharing=locked \
    --mount=type=cache,id=webmonitoring-pnpm-store,target=/root/.local/share/pnpm/store/v11,sharing=locked \
    corepack enable && pnpm install --filter @webmonitoring/console... --frozen-lockfile
COPY apps/console apps/console
# Reusing the corepack cache keeps the pnpm version pinned by the install step
# instead of re-downloading it from the registry on every build.
RUN --mount=type=cache,id=webmonitoring-corepack,target=/root/.cache/node/corepack,sharing=locked \
    pnpm --filter @webmonitoring/console build

FROM nginx:1.29-alpine
COPY --from=build /app/apps/console/dist /usr/share/nginx/html
COPY apps/console/nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 8080
