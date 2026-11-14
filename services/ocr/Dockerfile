# Build context: the repo root (the job imports libs/).
#   docker build -f services/ocr/Dockerfile -t ocr-container .
FROM golang:1.26-trixie AS build
WORKDIR /src
COPY libs/ libs/
COPY services/ocr/go.mod services/ocr/go.sum services/ocr/
RUN cd services/ocr && go mod download
COPY services/ocr/ services/ocr/
# PDFium runs as WebAssembly inside the binary, so no cgo.
RUN cd services/ocr && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ocr .

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tesseract-ocr tesseract-ocr-eng tesseract-ocr-ell \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --no-create-home ocr
COPY --from=build /out/ocr /usr/local/bin/ocr
USER 10001
ENTRYPOINT ["/usr/local/bin/ocr"]
