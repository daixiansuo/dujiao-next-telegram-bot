# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG APP_VERSION=1.0.0-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags="-s -w -X main.version=${APP_VERSION}" -o /out/dujiao-bot ./cmd/bot
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags="-s -w" -o /out/dujiao-compatcheck ./cmd/compatcheck

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S bot \
    && adduser -S -G bot bot \
    && mkdir -p /var/lib/dujiao-bot \
    && chown bot:bot /var/lib/dujiao-bot
USER bot
COPY --from=build /out/dujiao-bot /usr/local/bin/dujiao-bot
COPY --from=build /out/dujiao-compatcheck /usr/local/bin/dujiao-compatcheck
EXPOSE 8444
ENTRYPOINT ["/usr/local/bin/dujiao-bot"]
