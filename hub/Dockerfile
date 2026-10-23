FROM golang:1.26-alpine@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba AS builder
# Stamped by hub-docker.yml (release tag or dev-<sha>); `vaultsync-hub version`
# prints it.
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY pake ./pake
COPY pairing ./pairing
COPY syncthing ./syncthing
COPY join ./join
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=${VERSION}" -o /vaultsync-hub

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /vaultsync-hub /usr/local/bin/vaultsync-hub
# State dir exists in the image so a bind mount is optional for quick tests;
# the compose stack mounts ./hub here. Runs as whatever uid compose sets
# (PUID:PGID), which must match the Syncthing container to read config.xml.
COPY --from=builder --chown=65534:65534 /tmp /var/lib/vaultsync-hub
ENV VAULTSYNC_HUB_STATE=/var/lib/vaultsync-hub/state.json
ENTRYPOINT ["/usr/local/bin/vaultsync-hub"]
CMD ["serve"]
