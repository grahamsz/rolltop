FROM node:20-bookworm AS frontend

WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json vite.config.ts ./
COPY frontend ./frontend
RUN npm run build

FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mailmirror ./cmd/mailmirror

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates tzdata \
	&& rm -rf /var/lib/apt/lists/* \
	&& groupadd --system --gid 10001 mailmirror \
	&& useradd --system --uid 10001 --gid 10001 --home-dir /nonexistent --shell /usr/sbin/nologin mailmirror \
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
