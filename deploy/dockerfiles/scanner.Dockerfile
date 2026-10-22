# Imagen del escáner: CLI Go + worker Python de extracción (subproceso aislado).
# TODO Fase 4: fijar imágenes base por digest (@sha256:...) en CI.
FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.1.0-fase1
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/scanner ./cmd/scanner

FROM python:3.12-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
# OCR local (Fase 2): Tesseract con español e inglés. Los modelos van en la imagen;
# nunca se descargan en tiempo de ejecución (el escáner corre sin red).
RUN apt-get update \
 && apt-get install -y --no-install-recommends tesseract-ocr tesseract-ocr-spa tesseract-ocr-eng \
 && rm -rf /var/lib/apt/lists/*
COPY workers/extractor/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt && rm /tmp/requirements.txt \
 && useradd --system --uid 10001 --no-create-home --shell /usr/sbin/nologin arsop \
 && mkdir -p /var/tmp/arsop && chown 10001:10001 /var/tmp/arsop && chmod 0700 /var/tmp/arsop
COPY --from=build /out/scanner /usr/local/bin/scanner
COPY workers/extractor/extract.py /opt/arsop/workers/extractor/extract.py
# Datos ficticios de ejemplo (fuente "ejemplo") y preparación automática (servicio setup).
COPY tests/fixtures/synthetic/tree /opt/arsop/ejemplo
COPY tests/fixtures/synthetic/ocr /opt/arsop/ejemplo/escaneados
COPY deploy/docker-setup.sh /usr/local/bin/arsop-setup
RUN chmod -R a-w /opt/arsop/ejemplo && chmod 0755 /usr/local/bin/arsop-setup
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/scanner"]
CMD ["--help"]
