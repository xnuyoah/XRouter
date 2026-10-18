FROM golang:1.26-bookworm AS builder

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends build-essential git && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" -o ./xrouter ./cmd/server/

FROM debian:bookworm

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

LABEL org.opencontainers.image.title="XRouter" \
      org.opencontainers.image.description="OpenAI/Gemini/Claude/Codex compatible proxy gateway" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates curl && rm -rf /var/lib/apt/lists/*

RUN mkdir /xrouter

COPY --from=builder ./app/xrouter /xrouter/xrouter

COPY config.example.yaml /xrouter/config.example.yaml

# The management UI is built from webui/ into a single file and served from disk.
# It is never downloaded or auto-updated at runtime.
COPY static/index.html /xrouter/static/index.html

WORKDIR /xrouter

EXPOSE 11016

ENV TZ=Asia/Shanghai

RUN cp /usr/share/zoneinfo/${TZ} /etc/localtime && echo "${TZ}" > /etc/timezone

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -fsS http://127.0.0.1:11016/healthz >/dev/null || exit 1

CMD ["./xrouter"]
