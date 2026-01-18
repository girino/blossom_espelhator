# 🌺 Blossom Espelhator Tabajara

A media server proxy for the Blossom protocol (used by Nostr clients) that forwards files to multiple upstream Blossom servers instead of storing them locally. Provides redundancy, load distribution, health monitoring, and unified API access.

## Features

- **Redundancy**: Forwards uploads to multiple upstream Blossom servers simultaneously
- **Load Distribution**: Distributes download requests across healthy upstream servers
- **Health Monitoring**: Tracks server health and marks unhealthy servers after consecutive failures
- **Statistics**: Aggregates statistics from all upstream servers
- **Unified API**: Single endpoint for multiple upstream Blossom servers
- **All Blossom Endpoints**: Supports upload, download, list, delete, mirror, and preflight checks
- **BUD-08 & NIP-94**: Returns proper tags with `nip94` array including URL tags and NIP-94 metadata
- **Minimal Cache**: In-memory cache for hash-to-server mappings
- **Thread-safe Operations**: Safe for concurrent requests
- **Web Dashboard**: Built-in home page with health status and statistics
- **Docker Support**: Ready-to-use Dockerfile and docker-compose configuration

## Architecture

The proxy acts as an intermediary between Nostr clients and multiple upstream Blossom servers:

- **On upload**: Receives files and forwards them to multiple upstream servers (minimum configurable threshold)
- **On download**: Redirects requests to one of the healthy upstream servers that has the file
- **On mirror**: Forwards mirror requests to upstream servers that support the mirror endpoint (BUD-04)
- **On list**: Queries all upstream servers and merges/deduplicates results
- **On delete**: Forwards delete requests to all upstream servers that have the file
- Maintains a cache mapping blob hashes to available upstream servers

## Installation

### Building from Source

```bash
go build -o blossom_espelhator ./cmd/server
```

### Docker

The project includes Docker support:

```bash
# Build and run with docker-compose
docker-compose -f docker-compose.prod.yml up -d

# View logs
docker-compose -f docker-compose.prod.yml logs -f

# Stop
docker-compose -f docker-compose.prod.yml down
```

The Docker setup includes:
- Multi-stage build for small image size
- Health checks with autoheal support
- Project-specific labels for container management
- Read-only config mount

## Configuration

Create `config/config.yaml` from `config/config.example.yaml`:

```bash
cp config/config.example.yaml config/config.yaml
```

### Configuration Options

```yaml
# Upstream Blossom servers to forward uploads to
upstream_servers:
  - url: "https://blossom1.example.com"
    priority: 1
    supports_mirror: true          # BUD-04: Mirroring endpoint (PUT /mirror)
    supports_upload_head: true     # BUD-06: Upload preflight (HEAD /upload)
  - url: "https://blossom2.example.com"
    priority: 2
    supports_mirror: false         # Server doesn't support mirror
    supports_upload_head: true

# Proxy server configuration
server:
  listen_addr: ":8080"             # Address to listen on
  min_upload_servers: 2            # Minimum servers that must succeed for upload
  redirect_strategy: "round_robin" # Download server selection: "round_robin", "random", "health_based"
  timeout: 30s                     # Timeout for upstream requests
  max_retries: 3                   # Maximum retries for failed requests
  max_failures: 5                  # Consecutive failures before marking server unhealthy
```

### Server Capabilities

- `supports_mirror`: If `true`, the server supports BUD-04 `/mirror` endpoint (optional)
- `supports_upload_head`: If `true`, the server supports BUD-06 `HEAD /upload` preflight checks (optional)

## Running

### From Binary

```bash
./blossom_espelhator -config config/config.yaml

# With verbose logging
./blossom_espelhator -config config/config.yaml -v
```

### Command Line Options

- `-config <path>`: Path to configuration file (default: `config/config.yaml`)
- `-v` or `--verbose`: Enable verbose debug logging

## API Endpoints

### Web Dashboard

- **GET /** - Home page with health status, statistics, and documentation

### Health & Statistics

- **GET /health** - Health check endpoint (returns JSON)
  - Returns `200 OK` if system is healthy (enough healthy servers)
  - Returns `503 Service Unavailable` if system is unhealthy
  - Includes per-server health status and consecutive failures

- **GET /stats** - Statistics endpoint (returns JSON)
  - Aggregated statistics from all upstream servers
  - Per-server operation counts (uploads, downloads, mirrors, deletes, lists)
  - Success/failure counts and consecutive failures

### Blossom Protocol Endpoints

- **PUT /upload** - Upload a file (forwards to multiple upstream servers)
  - Requires Nostr authentication (kind 24242 event)
  - Forwards to at least `min_upload_servers` upstream servers
  - Returns response with `nip94` array containing URLs and metadata

- **HEAD /upload** - Upload preflight check (BUD-06)
  - Headers: `X-SHA-256`, `X-Content-Length`, `X-Content-Type`
  - Checks if upstream servers would accept the upload

- **PUT /mirror** - Mirror a blob (BUD-04)
  - Request body: `{"url": "<blob-url>"}`
  - Only forwards to servers with `supports_mirror: true`
  - Returns response with `nip94` array

- **GET /list/<pubkey>** - List files for a pubkey
  - Queries all upstream servers in parallel
  - Merges and deduplicates results based on `sha256`
  - Returns list with `nip94` tags for each item

- **GET /<sha256>.<ext>** - Download file
  - Redirects to one of the upstream servers that has the file
  - Uses configured redirect strategy (round_robin, random, health_based)

- **HEAD /<sha256>.<ext>** - Check file existence
  - Proxies HEAD request to upstream server
  - Returns headers and status code from upstream

- **DELETE /<sha256>** - Delete file
  - Forwards delete to all upstream servers that have the file
  - Removes from cache after successful deletion

## Response Format

All endpoints that return blob metadata include a `nip94` array with tags:

```json
{
  "url": "https://server.com/abc123.png",
  "sha256": "abc123...",
  "size": 12345,
  "type": "image/png",
  "uploaded": 1234567890,
  "nip94": [
    ["url", "https://server1.com/abc123.png"],
    ["url", "https://server2.com/abc123.png"],
    ["x", "abc123..."],
    ["m", "image/png"]
  ]
}
```

- `["url", "<url>"]`: URLs from all upstream servers (BUD-08)
- `["x", "<hash>"]`: SHA256 hash (NIP-94)
- `["m", "<mime-type>"]`: MIME type (NIP-94)

## Health Monitoring

The proxy tracks health for each upstream server:

- **Consecutive Failures**: Counts consecutive operation failures per server
- **Unhealthy Threshold**: Server marked unhealthy when failures exceed `max_failures`
- **Auto Recovery**: Failures reset to 0 on successful operation
- **System Health**: System is unhealthy if fewer than `min_upload_servers` are healthy

## Statistics

The `/stats` endpoint provides:

- **Per-server statistics**:
  - Operation counts (uploads, downloads, mirrors, deletes, lists)
  - Success/failure counts
  - Consecutive failures
  - Health status
  - Last success/failure timestamps

- **Aggregated totals**: Sum of all operations across all servers

## Usage with Nostr Clients

Configure your Nostr client to use this proxy server:

1. Set Blossom server URL to your proxy address (e.g., `http://blossom.example.com`)
2. Files uploaded through your client will be forwarded to multiple upstream servers
3. Downloads automatically use the best available upstream server

### Helper Scripts

The `scripts/` directory includes helper scripts:

- `gen_auth_header.sh` - Generate Nostr authentication headers for API requests
- `upload_file.sh` - Upload files with proper authentication
- `mirror_hash.sh` - Mirror blobs using the mirror endpoint

## Development

### Project Structure

```
blossom_espelhator/
├── cmd/server/          # Main application entry point
├── internal/
│   ├── cache/          # In-memory cache implementation
│   ├── client/         # HTTP client for upstream servers
│   ├── config/         # Configuration loading
│   ├── handler/        # HTTP request handlers
│   ├── stats/          # Statistics and health tracking
│   └── upstream/       # Upstream server management
├── config/             # Configuration files
├── scripts/            # Helper scripts
└── Dockerfile          # Docker build configuration
```

### Building

```bash
# Build binary
go build -o blossom_espelhator ./cmd/server

# Build for Docker
docker build -t blossom-espelhator .
```

## License

See [LICENSE](LICENSE) file for details.

Original license from [http://girino.org/license/](http://girino.org/license/)
