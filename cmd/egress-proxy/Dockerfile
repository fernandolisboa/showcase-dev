# Platform-owned egress-proxy sidecar image (ADR-0011). A tiny, statically-linked
# Go forward proxy on scratch — no shell, no package manager, minimal surface for
# a service that untrusted Owner code can reach. CGO disabled so DNS uses the pure
# Go resolver (reads the /etc/resolv.conf Docker injects); no CA certs are needed
# because the proxy only forwards plain HTTP and tunnels TLS via CONNECT (it never
# originates TLS itself).
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /egress-proxy ./cmd/egress-proxy

FROM scratch
COPY --from=build /egress-proxy /egress-proxy
EXPOSE 8888
ENTRYPOINT ["/egress-proxy"]
