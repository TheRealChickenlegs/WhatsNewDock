# syntax=docker/dockerfile:1

# ---- Frontend build -------------------------------------------------------
FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- Backend build --------------------------------------------------------
FROM golang:1.27-alpine AS build
ARG VERSION=dev
WORKDIR /src
# Cache module downloads first.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed the freshly-built frontend assets.
RUN rm -rf internal/webui/dist && cp -r /app/web/dist internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/whatsnewdock ./cmd/whatsnewdock

# ---- Volume ownership setup (for named volumes as non-root) ---------------
FROM alpine:3.20 AS volsetup
RUN mkdir -p /data && chown 65532:65532 /data

# ---- Final hardened image -------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/whatsnewdock /whatsnewdock
# Seed /data ownership so named volumes inherit it for the nonroot user.
COPY --from=volsetup --chown=nonroot:nonroot /data /data

EXPOSE 8080
VOLUME ["/data"]
USER nonroot:nonroot
ENTRYPOINT ["/whatsnewdock"]
