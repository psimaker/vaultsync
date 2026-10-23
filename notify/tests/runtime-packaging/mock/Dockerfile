FROM golang:1.26-alpine@sha256:27f2d3b83cfc5803c8078f8e730af0f7c431bf067afaec8f60a36fb763187cba AS builder

WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /runtime-packaging-mock main.go

FROM scratch
COPY --from=builder /runtime-packaging-mock /runtime-packaging-mock
ENTRYPOINT ["/runtime-packaging-mock"]
