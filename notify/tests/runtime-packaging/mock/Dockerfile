FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder

WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /runtime-packaging-mock main.go

FROM scratch
COPY --from=builder /runtime-packaging-mock /runtime-packaging-mock
ENTRYPOINT ["/runtime-packaging-mock"]
