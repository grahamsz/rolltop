FROM node:20-alpine AS frontend

WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json vite.config.ts vite.plugins.config.ts ./
COPY frontend ./frontend
COPY plugins ./plugins
RUN npm run build

FROM golang:1.25-alpine AS build
RUN apk add --no-cache build-base

WORKDIR /src
ARG ROLLTOP_VERSION=latest
ARG ROLLTOP_BUILD_DATE=
ARG ROLLTOP_COMMIT=
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w -X rolltop/backend/buildinfo.Version=${ROLLTOP_VERSION} -X rolltop/backend/buildinfo.BuildDate=${ROLLTOP_BUILD_DATE} -X rolltop/backend/buildinfo.Commit=${ROLLTOP_COMMIT}" -o /out/rolltop ./cmd/rolltop
RUN set -eu; \
	plugins='attachment_preview bimi_brand_icons gravatar_sender_icons language_search mail_filters oidc one_click_unsubscribe remote_image_blocklist remote_imap_sync trusted_image_sources client_side_pgp mail_mcp'; \
	for plugin in $plugins; do \
		mkdir -p "/out/plugins/${plugin}/backend"; \
		CGO_ENABLED=1 GOOS=linux go build -buildmode=plugin -trimpath -ldflags="-s -w" -o "/out/plugins/${plugin}/backend/${plugin}.so" "./plugins/${plugin}/backend"; \
	done

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata poppler-utils antiword \
	&& addgroup -S -g 10001 rolltop \
	&& adduser -S -D -H -u 10001 -G rolltop -s /sbin/nologin rolltop \
	&& mkdir -p /data \
	&& chown -R rolltop:rolltop /data

WORKDIR /app
COPY --from=build /out/rolltop /usr/local/bin/rolltop
COPY --from=frontend /src/frontend/dist /app/frontend/dist
COPY --from=frontend /src/plugins /app/plugins
COPY --from=build /out/plugins /app/plugins

USER rolltop
EXPOSE 8080
VOLUME ["/data"]

ENV ROLLTOP_ADDR=:8080
ENV ROLLTOP_DATA_DIR=/data

ENTRYPOINT ["/usr/local/bin/rolltop"]
