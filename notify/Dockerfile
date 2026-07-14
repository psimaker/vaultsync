FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder
# Stamped by docker.yml (release tag or dev-<sha>); --version prints it and the
# installer shows old -> new on upgrades (#87).
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=${VERSION}" -o /vaultsync-notify

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
# Upgrade base packages: security fixes often land in the apk repos before
# the alpine base image is re-released (e.g. CVE-2026-45447 in libcrypto3).
RUN apk --no-cache upgrade && \
    apk add --no-cache ca-certificates && \
    adduser -D -H appuser
COPY --from=builder /vaultsync-notify /usr/local/bin/
USER appuser
HEALTHCHECK --interval=30s --timeout=20s --start-period=20s --retries=3 \
    CMD vaultsync-notify --healthcheck || exit 1
ENTRYPOINT ["vaultsync-notify"]
