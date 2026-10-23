FROM golang:1.26-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS builder

WORKDIR /src
COPY main.go .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /runtime-packaging-mock main.go

FROM scratch
COPY --from=builder /runtime-packaging-mock /runtime-packaging-mock
ENTRYPOINT ["/runtime-packaging-mock"]
