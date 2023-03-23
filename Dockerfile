FROM golang:1.18 AS build

COPY . /code

WORKDIR /code

RUN make build

FROM ubuntu:bionic

COPY --from=build /code/tx-submit-api-mirror /usr/local/bin/

ENTRYPOINT ["/usr/local/bin/tx-submit-api-mirror"]
