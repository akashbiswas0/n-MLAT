# n-MLAT

`n-MLAT` is a decentralized aircraft localization demo built on the Neuron network, 4DSky data streams, and Hedera-based peer discovery. It consumes live Mode-S observations from distributed sellers, correlates the same transmission across multiple sensors, solves aircraft position with MLAT/TDOA, and pushes live fixes to a browser map.

## What It Does

- Discovers data sellers through the Neuron SDK and Hedera-backed network flow
- Receives live Mode-S packets over libp2p / QUIC
- Correlates the same transmitted frame across sensors
- Runs multilateration using sensor timing and geometry, not broadcast lat/lon
- Streams solved aircraft fixes to a live map at `http://localhost:8080`
- Optionally publishes MLAT audit records to Hedera Consensus Service

## Project Layout

- `main.go`: app entrypoint, packet ingest, MLAT pipeline wiring
- `mlat/`: observation correlation, coordinate transforms, solver, seller location overrides
- `server/`: HTTP + WebSocket map backend
- `static/`: browser map UI assets
- `hedera-main/`: optional Hedera audit publisher

## Requirements

- Go `1.24.10`
- Buyer credentials in `.buyer-env`
- Seller location overrides in a local `location-override.json`
- A network path that accepts inbound `UDP 61336`

If your home connection is behind CGNAT, run the buyer on a VPS with a public IPv4.

## Local Configuration

The repo intentionally does **not** track real override data.

1. Create `.buyer-env` with the credentials you received from the challenge organizers.
2. Copy `location-override.example.json` to `location-override.json`.
3. Replace the placeholder entries with the real seller public keys and true sensor coordinates.

Example:

```bash
cp location-override.example.json location-override.json
```

## Run

Start the buyer:

```bash
go run . --port=61336 --mode=peer --buyer-or-seller=buyer --list-of-sellers-source=env --envFile=.buyer-env
```

Or build a binary:

```bash
go build -o hedera4d .
./hedera4d --port=61336 --mode=peer --buyer-or-seller=buyer --list-of-sellers-source=env --envFile=.buyer-env
```

When the node is healthy, you should see:

- a public libp2p listen address on `UDP 61336`
- seller stream connections opening
- MLAT fixes being logged
- the map UI available at `http://localhost:8080`

## Test

```bash
go test ./...
```

If your environment blocks the default Go build cache path, use:

```bash
GOCACHE=/tmp/go-build-cache go test ./...
```

## Notes

- The solver uses Mode-S timing and sensor geometry only; it does not depend on broadcast aircraft position.
- `location-override.json` is intentionally ignored by Git because it contains seller-specific data.
- Hedera publishing is optional. If the topic cannot be created or credentials are invalid, the app will continue running without the audit publisher.
