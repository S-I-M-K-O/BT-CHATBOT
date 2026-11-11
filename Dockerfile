# Browser assets (onnxruntime-web, the CAM++ speaker model, pdf.js,
# Readability.js) are not committed: this stage downloads them and checks
# each file against the sha256 pinned in scripts/fetch-assets.sh.
# FETCH_ASSETS=0 builds offline without them (voice ID, PDF text and the
# Readability extractor are then unavailable; everything else works).
FROM alpine:3.20 AS assets
ARG FETCH_ASSETS=1
RUN apk add --no-cache curl
COPY scripts/fetch-assets.sh /fetch-assets.sh
RUN mkdir -p /out/vendor && if [ "$FETCH_ASSETS" = "1" ]; then sh /fetch-assets.sh /out; fi

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bt-chatbot ./cmd/hermes

FROM debian:bookworm-slim
WORKDIR /app

# Piper runs for every TTS request, so the release tarball is pinned by digest:
# a re-uploaded or tampered asset fails the build instead of shipping.
ARG TARGETARCH
ARG PIPER_VERSION=2023.11.14-2
ARG PIPER_SHA256_x86_64=a50cb45f355b7af1f6d758c1b360717877ba0a398cc8cbe6d2a7a3a26e225992
ARG PIPER_SHA256_aarch64=fea0fd2d87c54dbc7078d0f878289f404bd4d6eea6e7444a77835d1537ab88eb
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl libgomp1 \
 && ARCH=$( [ "$TARGETARCH" = "arm64" ] && echo aarch64 || echo x86_64 ) \
 && SHA=$( [ "$ARCH" = aarch64 ] && echo "$PIPER_SHA256_aarch64" || echo "$PIPER_SHA256_x86_64" ) \
 && curl -fsSL -o /tmp/piper.tgz "https://github.com/rhasspy/piper/releases/download/${PIPER_VERSION}/piper_linux_${ARCH}.tar.gz" \
 && echo "$SHA  /tmp/piper.tgz" | sha256sum -c - \
 && mkdir -p /opt/piper \
 && tar xzf /tmp/piper.tgz -C /opt/piper --strip-components=1 \
 && rm /tmp/piper.tgz \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /bt-chatbot /app/bt-chatbot
COPY web/static /app/static
COPY --from=assets /out/vendor /app/static/vendor
COPY voices /app/voices
# Run unprivileged. uid 1000 matches the usual first Linux user, so files in
# the ./data bind mount stay editable from the host (see docker-compose.yml).
RUN useradd --uid 1000 --user-group --no-create-home --home-dir /app --shell /usr/sbin/nologin hermes \
 && mkdir -p -m 0700 /app/data /app/data/gen \
 && chown -R hermes:hermes /app/data
USER hermes

ENV PORT=8080
ENV SETTINGS_FILE=/app/data/settings.json
ENV PIPER_BIN=/opt/piper/piper
ENV VOICE_DIR=/app/voices
ENV STATIC_DIR=/app/static
ENV LD_LIBRARY_PATH=/opt/piper

EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD curl -fsS "http://localhost:${PORT}/api/health" || exit 1
CMD ["/app/bt-chatbot"]