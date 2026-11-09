# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Go cross-compiles on the build platform, so multi-arch builds need no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/gw2tools . \
    && mkdir /out/data

# The binary is static, so the runtime image only needs CA certificates (for HTTPS
# to the GW2 API) and a writable /data for the default database path.
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build --chown=65534:65534 /out/data /data
COPY --from=build /out/gw2tools /gw2tools
ENV GW2TOOLS_DB=/data/gw2tools.db \
    GW2TOOLS_LISTEN=:8080
EXPOSE 8080
USER 65534:65534
ENTRYPOINT ["/gw2tools"]
CMD ["serve"]
