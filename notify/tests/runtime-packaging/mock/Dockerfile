FROM golang:1.26-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS builder

WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /runtime-packaging-mock main.go

FROM scratch
COPY --from=builder /runtime-packaging-mock /runtime-packaging-mock
ENTRYPOINT ["/runtime-packaging-mock"]
