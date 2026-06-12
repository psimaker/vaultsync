FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod *.go ./
RUN CGO_ENABLED=0 go build -o /vaultsync-notify

FROM alpine:3.23
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
