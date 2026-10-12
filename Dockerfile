# syntax=docker/dockerfile:1

# Compilation croisée sur la plateforme de build : pas d'émulation pour les images multi-architectures.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /depdash ./cmd/depdash

# Image finale : le binaire seul (templates, CSS et htmx y sont embarqués).
FROM gcr.io/distroless/static-debian12
COPY --from=build /depdash /depdash
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["/depdash", "healthcheck"]
ENTRYPOINT ["/depdash"]
CMD ["serve"]
