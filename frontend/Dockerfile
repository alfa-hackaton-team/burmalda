FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
ARG VITE_DATA_MODE=demo
ENV VITE_DATA_MODE=$VITE_DATA_MODE
ARG VITE_AUTH_START_URL=
ENV VITE_AUTH_START_URL=$VITE_AUTH_START_URL
RUN npm run build

FROM nginx:1.28-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
