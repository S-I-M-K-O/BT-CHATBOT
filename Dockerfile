FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN go mod download
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bt-chatbot .

FROM debian:bookworm-slim
WORKDIR /app

ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl libgomp1 \
 && ARCH=$( [ "$TARGETARCH" = "arm64" ] && echo aarch64 || echo x86_64 ) \
 && curl -fsSL -o /tmp/piper.tgz "https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_${ARCH}.tar.gz" \
 && mkdir -p /opt/piper \
 && tar xzf /tmp/piper.tgz -C /opt/piper --strip-components=1 \
 && rm /tmp/piper.tgz \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /bt-chatbot /app/bt-chatbot
COPY static /app/static
COPY voices /app/voices
RUN mkdir -p /app/data

ENV PORT=8080
ENV SETTINGS_FILE=/app/data/settings.json
ENV PIPER_BIN=/opt/piper/piper
ENV VOICE_DIR=/app/voices
ENV LD_LIBRARY_PATH=/opt/piper

EXPOSE 8080
CMD ["/app/bt-chatbot"]