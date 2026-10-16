FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/kt-import ./cmd/kt-import

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10002 --home-dir /tmp --shell /usr/sbin/nologin ktimport
COPY --from=build /out/kt-import /usr/local/bin/kt-import
COPY testdata /opt/kt-import/testdata
USER ktimport
WORKDIR /opt/kt-import
ENTRYPOINT ["kt-import"]
CMD ["--fixture", "/opt/kt-import/testdata/export.json"]
