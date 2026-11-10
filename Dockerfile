# Build stage: whisper.cpp static libs (CPU + OpenMP, the Linux defaults) and
# the Go binary with cgo. The whisper.cpp submodule must be checked out
# (CI: actions/checkout with submodules: recursive).
FROM golang:1.23-bookworm AS build

RUN apt-get update \
 && apt-get install -y --no-install-recommends cmake build-essential git \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# whisper.cpp first so this heavy layer stays cached across Go source edits.
COPY Makefile go.mod go.sum ./
COPY third_party/ ./third_party/
RUN make whisper-lib

COPY cmd ./cmd
COPY embed.go record-zoom.html ./
COPY internal ./internal
RUN make build

# Runtime: ffmpeg drives decode/silencedetect/RMS; libgomp is whisper.cpp's
# OpenMP runtime on Linux.
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg libgomp1 ca-certificates \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /src/bin/zoom-taker /usr/local/bin/zoom-taker

ENV HOST=0.0.0.0 \
    PORT=8899 \
    ZOOM_TAKER_MODEL_CACHE_DIR=/app/model-cache \
    ZOOM_TAKER_RECORDINGS_DIR=/app/recordings \
    ZOOM_TAKER_OUTPUT_DIR=/app/output

EXPOSE 8899
CMD ["zoom-taker", "serve"]
