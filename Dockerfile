FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kuztds-engine ./cmd/engine \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kuztds-admin ./cmd/admin \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kuztds-cron ./cmd/cron

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 --home-dir /var/lib/kuztds --shell /usr/sbin/nologin kuztds \
    && mkdir -p /var/lib/kuztds/data /var/lib/kuztds/keys

COPY --from=build /out/kuztds-engine /out/kuztds-admin /out/kuztds-cron /usr/local/bin/
COPY deploy/docker-entrypoint.sh /usr/local/bin/kuztds-entrypoint
COPY --chown=kuztds:kuztds configs/groups.example.json /var/lib/kuztds/groups.json

RUN chmod 755 /usr/local/bin/kuztds-entrypoint /usr/local/bin/kuztds-engine /usr/local/bin/kuztds-admin /usr/local/bin/kuztds-cron \
    && chown -R kuztds:kuztds /var/lib/kuztds

USER kuztds
WORKDIR /var/lib/kuztds

ENTRYPOINT ["kuztds-entrypoint"]
