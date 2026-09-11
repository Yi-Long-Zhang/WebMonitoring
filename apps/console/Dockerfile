FROM node:24-alpine AS build
WORKDIR /app
COPY package.json pnpm-workspace.yaml ./
COPY apps/console/package.json apps/console/package.json
RUN corepack enable && pnpm install --filter @webmonitoring/console... --frozen-lockfile=false
COPY apps/console apps/console
RUN pnpm --filter @webmonitoring/console build

FROM nginx:1.29-alpine
COPY --from=build /app/apps/console/dist /usr/share/nginx/html
COPY apps/console/nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 8080
