# Build context: this directory.
#   docker build -t ocr-container .
FROM golang:1.26-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# PDFium runs as WebAssembly inside the binary, so no cgo.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ocr_container .

FROM debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tesseract-ocr tesseract-ocr-eng tesseract-ocr-ell \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --no-create-home ocr
COPY --from=build /out/ocr_container /usr/local/bin/ocr_container
USER 10001
ENTRYPOINT ["/usr/local/bin/ocr_container"]
