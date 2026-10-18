# Frontend image (Vue 3 + Vite + Bulma, served by nginx)
FROM node:20-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm install && npm cache clean --force
COPY . .

RUN npm run build

FROM scratch
COPY --from=builder /app/dist /
