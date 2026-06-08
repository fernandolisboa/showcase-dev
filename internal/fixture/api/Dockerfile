# API service of the build-from-source fixture (#15/#16/#17). A real DB-backed
# demo: the platform runs it once with `-seed` to migrate and seed the fresh
# per-Session Postgres before readiness, then as an HTTP server that reads the
# seeded rows back under "/api". A non-root, all-caps-dropped, read-only-rootfs
# container at runtime (the platform bakes that in), so the binary is static
# (CGO disabled) and the final image is scratch.
#
# The Go module files are committed as *.txt: internal/fixture embeds this
# directory with //go:embed, which refuses to descend into a nested Go module, so
# the real names exist only here. We rename them back before compiling.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod.txt go.mod
COPY go.sum.txt go.sum
RUN go mod download
COPY main.go.txt main.go
RUN CGO_ENABLED=0 go build -trimpath -o /api .

FROM scratch
COPY --from=build /api /api
EXPOSE 8080
USER 1000:1000
ENTRYPOINT ["/api"]
