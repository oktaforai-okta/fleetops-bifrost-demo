# Pinned to the same Go version as Bifrost core v1.8.4 declares. The server does not
# strictly need to match, since it is a separate process rather than a loaded plugin,
# but keeping one Go version across the repo removes a variable.
FROM golang:1.27 AS build

WORKDIR /src
COPY go.mod ./
COPY *.go ./

# CGO off so the result runs on a distroless base with no libc to match.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fleetops .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fleetops /fleetops
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/fleetops"]
