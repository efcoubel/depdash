# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /depdash ./cmd/depdash

# Image finale : le binaire seul (templates, CSS et htmx y sont embarqués).
FROM gcr.io/distroless/static-debian12
COPY --from=build /depdash /depdash
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["/depdash", "healthcheck"]
ENTRYPOINT ["/depdash"]
CMD ["serve"]
