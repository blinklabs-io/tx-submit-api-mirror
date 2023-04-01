FROM cgr.dev/chainguard/go:1.19
WORKDIR /app
COPY . .
RUN make build

FROM cgr.dev/chainguard/glibc-dynamic
COPY --from=0 /app/tx-submit-api-mirror /bin/
ENTRYPOINT ["tx-submit-api-mirror"]
