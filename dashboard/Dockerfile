# Multi-stage build for the Synapse dashboard (Next.js).
#
# We bake NEXT_PUBLIC_SYNAPSE_URL at build time because Next inlines public
# env vars into the client bundle. Override it at compose time via
# --build-arg if you're shipping the image to a non-default URL.

FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:22-alpine AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
ARG NEXT_PUBLIC_SYNAPSE_URL=http://localhost:8080
ENV NEXT_PUBLIC_SYNAPSE_URL=$NEXT_PUBLIC_SYNAPSE_URL
# NEXT_PUBLIC_CONVEX_DASHBOARD_URL is the URL where the OPEN-SOURCE
# Convex Dashboard (the "data/functions/logs" UI for a single
# deployment) is reachable. Synapse hosts it as a sibling service
# in docker-compose; setup.sh wires this to http://<vps>:6791 (or
# https://<domain>:6791 with TLS) so the "Open dashboard" button
# generates a link operators can actually open from a remote browser.
# Default targets a local-dev `docker run convex-dashboard` on 6791.
ARG NEXT_PUBLIC_CONVEX_DASHBOARD_URL=http://localhost:6791
ENV NEXT_PUBLIC_CONVEX_DASHBOARD_URL=$NEXT_PUBLIC_CONVEX_DASHBOARD_URL
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build

FROM node:22-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
ENV PORT=3000
COPY --from=builder /app/public ./public
COPY --from=builder /app/.next/standalone ./
COPY --from=builder /app/.next/static ./.next/static
EXPOSE 3000
USER node
CMD ["node", "server.js"]
