FROM golang:1.27-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS builder
# Stamped by docker.yml (release tag or dev-<sha>); --version prints it and the
# installer shows old -> new on upgrades (#87).
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=${VERSION}" -o /vaultsync-notify

FROM scratch
# The pinned Go builder already carries the CA bundle used by its HTTPS
# toolchain. Copying that exact file avoids a mutable apk repository during the
# release build and leaves no shell or package manager in the runtime image.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /vaultsync-notify /usr/local/bin/vaultsync-notify
USER 65534:65534
HEALTHCHECK --interval=30s --timeout=20s --start-period=20s --retries=3 \
    CMD ["/usr/local/bin/vaultsync-notify", "--healthcheck"]
ENTRYPOINT ["/usr/local/bin/vaultsync-notify"]
