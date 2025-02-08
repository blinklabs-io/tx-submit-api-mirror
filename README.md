# tx-submit-api-mirror

A simple Cardano transaction submission API service which mirrors all incoming
requests asynchronously to configured backend submission API services.

A simple HTTP API which accepts a CBOR encoded Cardano transaction as a
payload body and submits it to one or more configured backend transaction
submission API services.

## Usage

The recommended method of using this application is via the published
container images or application binaries.

Docker:
```
docker run -e BACKENDS=http://tx1,http://tx2 -p 8090:8090 ghcr.io/blinklabs-io/tx-submit-api-mirror
docker run -d \
  -e BACKENDS=http://tx1/api/submit/tx,https://tx2,http://tx3:8090 \
  -p 8080:8080 \
  ghcr.io/blinklabs-io/tx-submit-api-mirror
```

Binaries can be executed directly and are available from
[Releases](https://github.com/blinklabs-io/tx-submit-api-mirror/releases).

```
BACKENDS=http://tx1,http://tx2 ./tx-submit-api-mirror
```

### Configuration

Configuration can be done using either a `config.yaml` file or setting
environment variables. Our recommendation is environment variables to adhere
to the 12-factor application philisophy.

#### Environment variables

Configuration via environment variables can be broken into two sets of
variables. The first set controls the behavior of the application, while the
second set controls the connection to the backend submission APIs.

Application configuration:
- `API_LISTEN_ADDRESS` - Address to bind for API calls, all addresses if empty
    (default: empty)
- `API_LISTEN_PORT` - Port to bind for API calls (default: 8090)
- `CLIENT_TIMEOUT` - Timeout for async HTTP operations (default: 60000)
- `LOGGING_LEVEL` - Logging level for log output (default: info)
- `TLS_CERT_FILE_PATH` - SSL certificate to use, requires `TLS_KEY_FILE_PATH`
    (default: empty)
- `TLS_KEY_FILE_PATH` - SSL certificate key to use (default: empty)

Connection to the backends can be configured using general HTTP Cardano
transaction submission APIs or using infrastructure providers with a
transaction submission endpoint. All requests are sent async and non-blocking.

Backends configuration:
- `BACKENDS` - Comma separated list of HTTP Cardano transaction submission
    service API URIs

Maestro configuration:
- `MAESTRO_API_KEY` - API key to a Cardano project on Maestro
- `MAESTRO_NETWORK` - Named Cardano network to use (default: mainnet)
- `MAESTRO_TURBO_TX` - Enable use of Maestro TurboTx for transaction submission
    (default: false)

Connection to Maestro can be performed using specific named network shortcuts
for known network magic configurations. Supported named networks are:

- mainnet
- preprod
- preview

### Sending transactions

This implementation shares an API spec with IOHK's Haskell implementation. The
same instructions apply. Follow the steps to
[build and submit a transaction](https://github.com/input-output-hk/cardano-node/tree/master/cardano-submit-api#build-and-submit-a-transaction)

```
# Submit a binary tx.signed.cbor signed CBOR encoded transaction binary file
curl -X POST \
  --header "Content-Type: application/cbor" \
  --data-binary @tx.signed.cbor \
  http://localhost:8090/api/submit/tx
```

## Development

There is a Makefile to provide some simple helpers.

Run from checkout:
```
go run .
```

Create a binary:
```
make
```

Create a docker image:
```
make image
```
