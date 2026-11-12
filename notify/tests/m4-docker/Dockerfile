FROM golang:1.26-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go test -trimpath -c -o /m4-namespace.test .

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
COPY --from=builder /m4-namespace.test /usr/local/bin/m4-namespace.test
ENTRYPOINT ["/usr/local/bin/m4-namespace.test"]
