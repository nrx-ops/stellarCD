# Build stage
FROM node:20-alpine AS builder

WORKDIR /build

# Copy package files
COPY package.json package-lock.json* yarn.lock* ./

# Install dependencies
RUN npm ci || npm install

# Copy source
COPY . .

# Build
RUN npm run build

# Runtime stage
#
# nginx serves the static bundle and proxies /api to the operator's admin API.
# The vite dev server must not run in production: its `server.proxy` block only
# applies to `npm run dev`, so a statically served build has no proxy at all and
# every /api call would fall through to the SPA index.html.
FROM nginx:1.29-alpine

# Rendered to /etc/nginx/conf.d/default.conf on start-up by the base image's
# envsubst entrypoint. The filter keeps substitution to this one variable so
# nginx's own $host / $uri variables survive untouched.
ENV ADMIN_API_URL=http://stellarcd-admin-api:8080
ENV NGINX_ENVSUBST_FILTER=^ADMIN_API_URL$

COPY --from=builder /build/dist /usr/share/nginx/html
COPY nginx.conf.template /etc/nginx/templates/default.conf.template

EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:3000/healthz || exit 1
