# Next.js standalone image for the Consul nginx (WEB_IMAGE -> web:3000).
FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:22-alpine AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# Rewrites (/api/v1 → API) are fixed at build time; in the stack the API is the `api` container.
ARG API_URL=http://api:8080
ARG NEXT_PUBLIC_DEMO_OTP=123456
ENV NEXT_TELEMETRY_DISABLED=1 API_URL=$API_URL NEXT_PUBLIC_DEMO_OTP=$NEXT_PUBLIC_DEMO_OTP
RUN npm run build

FROM node:22-alpine AS run
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=3000 HOSTNAME=0.0.0.0
COPY --from=build /app/public ./public
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
USER node
EXPOSE 3000
CMD ["node", "server.js"]
