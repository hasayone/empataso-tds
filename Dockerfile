FROM golang:1.25-bookworm AS deps

WORKDIR /src

COPY go.mod go.sum ./
COPY internal/bridge/go.mod internal/bridge/go.mod
RUN go mod download

FROM deps AS test

RUN apt-get update \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

COPY . .

FROM deps AS build

COPY . .

ARG EMTDS_VERSION=
ARG EMTDS_COMMIT=
ARG EMTDS_COMMIT_DATE=
ARG EMTDS_CHANNEL=
ARG EMTDS_GOFLAGS=

RUN --mount=type=cache,target=/root/.cache/go-build \
    set -e; \
    LDFLAGS="-s -w -X github.com/egerkuzma/emtds/internal/config.version=${EMTDS_VERSION} -X github.com/egerkuzma/emtds/internal/config.commit=${EMTDS_COMMIT} -X github.com/egerkuzma/emtds/internal/config.commitDate=${EMTDS_COMMIT_DATE} -X github.com/egerkuzma/emtds/internal/config.channel=${EMTDS_CHANNEL}"; \
    CGO_ENABLED=0 GOOS=linux GOFLAGS="${EMTDS_GOFLAGS}" go build -trimpath -ldflags="${LDFLAGS}" -o /out/emtds-engine ./cmd/engine; \
    CGO_ENABLED=0 GOOS=linux GOFLAGS="${EMTDS_GOFLAGS}" go build -trimpath -ldflags="${LDFLAGS}" -o /out/emtds-admin ./cmd/admin; \
    CGO_ENABLED=0 GOOS=linux GOFLAGS="${EMTDS_GOFLAGS}" go build -trimpath -ldflags="${LDFLAGS}" -o /out/emtds-cron ./cmd/cron

FROM build AS mcp-dist

RUN set -e; \
    for target in linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64; do \
        os="${target%/*}"; arch="${target#*/}"; ext=""; \
        if [ "$os" = "windows" ]; then ext=".exe"; fi; \
        CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="-s -w" \
            -o "/dist/emtds-mcp-${os}-${arch}${ext}" ./cmd/mcp; \
    done

FROM scratch AS mcp-out

COPY --from=mcp-dist /dist /

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home-dir /var/lib/emtds --shell /usr/sbin/nologin emtds \
    && mkdir -p /var/lib/emtds/data /var/lib/emtds/keys \
    && ln -s /var/lib/emtds /var/lib/kuztds

COPY --from=build /out/emtds-engine /out/emtds-admin /out/emtds-cron /usr/local/bin/
COPY deploy/docker-entrypoint.sh /usr/local/bin/emtds-entrypoint
COPY --chown=emtds:emtds configs/groups.example.json /var/lib/emtds/groups.json

RUN chmod 755 /usr/local/bin/emtds-entrypoint /usr/local/bin/emtds-engine /usr/local/bin/emtds-admin /usr/local/bin/emtds-cron \
    && chown -R emtds:emtds /var/lib/emtds \
    && mkdir -p /var/lib/emtds-spool \
    && chown emtds:emtds /var/lib/emtds-spool \
    && chmod 0700 /var/lib/emtds-spool

USER emtds
WORKDIR /var/lib/emtds

ARG EMTDS_VERSION=
ARG EMTDS_COMMIT=
ARG EMTDS_COMMIT_DATE=

ENTRYPOINT ["emtds-entrypoint"]

LABEL org.opencontainers.image.revision=$EMTDS_COMMIT \
      org.opencontainers.image.version=$EMTDS_VERSION \
      org.opencontainers.image.created=$EMTDS_COMMIT_DATE
