# Blossom Espelhator

A media server proxy for the Blossom protocol (used by Nostr clients) that forwards files to multiple upstream Blossom servers instead of storing them locally.

## Features

- Forwards uploads to at least 2 upstream Blossom servers in parallel
- Redirects download requests to one of the upstream servers
- Supports all Blossom protocol endpoints: upload, download, list, and delete
- Minimal in-memory cache for hash-to-server mappings
- Thread-safe operations

## Architecture

The proxy acts as an intermediary between Nostr clients and multiple upstream Blossom servers:
- On upload: Receives files and forwards them to multiple upstream servers
- On download: Redirects requests to one of the upstream servers that has the file
- Maintains a cache mapping blob hashes to available upstream servers

## Configuration

See `config/config.example.yaml` for configuration options.

## Building

```bash
go build -o blossom_espelhator ./cmd/server
```

## Running

```bash
./blossom_espelhator -config config/config.yaml
```

## License

MIT
