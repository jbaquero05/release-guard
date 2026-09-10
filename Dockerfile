FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod ./
COPY main.go ./

RUN go build \
    -ldflags="-s -w" \
    -o release-guard \
    main.go


FROM alpine:3.21

RUN addgroup -S releaseguard && \
    adduser -S releaseguard -G releaseguard

WORKDIR /app

COPY --from=builder /app/release-guard /app/release-guard

USER releaseguard

EXPOSE 8080

ENTRYPOINT ["/app/release-guard"]
