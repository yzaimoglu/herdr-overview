FROM oven/bun:1.2.19 AS frontend
WORKDIR /app
COPY package.json bun.lock* ./
RUN bun install --frozen-lockfile
COPY astro.config.mjs tsconfig.json ./
COPY src ./src
COPY public ./public
RUN bun run build

FROM caddy:2.10-alpine
COPY --from=frontend /app/dist /srv/site
COPY Caddyfile /etc/caddy/Caddyfile
EXPOSE 80
