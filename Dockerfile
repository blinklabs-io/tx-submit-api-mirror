FROM ghcr.io/blinklabs-io/go:1.24.5-1 AS build

WORKDIR /code
COPY . .
RUN make build

FROM cgr.dev/chainguard/glibc-dynamic AS tx-submit-api-mirror
COPY --from=0 /code/tx-submit-api-mirror /bin/
ENTRYPOINT ["tx-submit-api-mirror"]
