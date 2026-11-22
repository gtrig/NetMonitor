# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /netmon ./cmd/netmon

FROM alpine:3.22
RUN apk add --no-cache nmap nmap-nselibs nmap-scripts ca-certificates tzdata libcap \
 && setcap cap_net_raw,cap_net_admin,cap_net_bind_service+eip /usr/bin/nmap \
 && adduser -D -H -u 10001 netmon \
 && mkdir -p /data /config && chmod 1777 /data
COPY --from=build /netmon /usr/local/bin/netmon
ENV NETMON_CONFIG=/config/config.yaml \
    NETMON_LISTEN=0.0.0.0:8080 \
    NETMON_DATA_DIR=/data \
    NMAP_PRIVILEGED=1
# Compose runs the container as your host user (NETMON_UID) so it can write config/.
USER 10001
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- "http://127.0.0.1:${NETMON_LISTEN##*:}/healthz" || exit 1
ENTRYPOINT ["/usr/local/bin/netmon"]
