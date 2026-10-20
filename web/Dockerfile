# Frontend image (Vue 3 + Vite + Bulma, served by nginx)
FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm install && npm cache clean --force
COPY . .

# API base URL baked into the build (the api subdomain in production).
ARG VITE_API_BASE=https://api.domain.tld
ENV VITE_API_BASE=$VITE_API_BASE

RUN npm run build

FROM scratch
COPY --from=builder /app/dist /
