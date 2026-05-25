FROM node:20-alpine AS frontend

WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json vite.config.ts ./
COPY frontend ./frontend
RUN npm run build

FROM golang:1.25-alpine AS build
RUN apk add --no-cache build-base

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mailmirror ./cmd/mailmirror

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata poppler-utils antiword \
	&& addgroup -S -g 10001 mailmirror \
	&& adduser -S -D -H -u 10001 -G mailmirror -s /sbin/nologin mailmirror \
	&& mkdir -p /data \
	&& chown -R mailmirror:mailmirror /data

WORKDIR /app
COPY --from=build /out/mailmirror /usr/local/bin/mailmirror
COPY --from=frontend /src/frontend/dist /app/frontend/dist

USER mailmirror
EXPOSE 8080
VOLUME ["/data"]

ENV MAILMIRROR_ADDR=:8080
ENV MAILMIRROR_DATA_DIR=/data

ENTRYPOINT ["/usr/local/bin/mailmirror"]
