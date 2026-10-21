# Imagen de la API: binario estático sobre distroless no-root. No ve archivos de origen.
# TODO Fase 4: fijar imágenes base por digest (@sha256:...) en CI.
FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /api
USER 65532:65532
ENTRYPOINT ["/api", "--file", "/etc/arsop/scanner.yaml"]
