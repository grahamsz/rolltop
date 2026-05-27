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
ARG ROLLTOP_VERSION=latest
ARG ROLLTOP_BUILD_DATE=
ARG ROLLTOP_COMMIT=
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w -X mailmirror/backend/buildinfo.Version=${ROLLTOP_VERSION} -X mailmirror/backend/buildinfo.BuildDate=${ROLLTOP_BUILD_DATE} -X mailmirror/backend/buildinfo.Commit=${ROLLTOP_COMMIT}" -o /out/rolltop ./cmd/mailmirror

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata poppler-utils antiword \
	&& addgroup -S -g 10001 rolltop \
	&& adduser -S -D -H -u 10001 -G rolltop -s /sbin/nologin rolltop \
	&& mkdir -p /data \
	&& chown -R rolltop:rolltop /data

WORKDIR /app
COPY --from=build /out/rolltop /usr/local/bin/rolltop
COPY --from=frontend /src/frontend/dist /app/frontend/dist

USER rolltop
EXPOSE 8080
VOLUME ["/data"]

ENV MAILMIRROR_ADDR=:8080
ENV MAILMIRROR_DATA_DIR=/data

ENTRYPOINT ["/usr/local/bin/rolltop"]
