# 🌺 Blossom Espelhator Tabajara

A media server proxy for the Blossom protocol (used by Nostr clients) that forwards files to multiple upstream Blossom servers instead of storing them locally. Provides redundancy, load distribution, health monitoring, and unified API access.

## Features

- **Redundancy**: Forwards uploads to multiple upstream Blossom servers simultaneously
- **Load Distribution**: Distributes download requests across healthy upstream servers
- **Health Monitoring**: Tracks server health and marks unhealthy servers after consecutive failures
- **System Resource Monitoring**: Monitors memory usage and goroutine counts with configurable limits
- **Statistics**: Aggregates statistics from all upstream servers with per-server operation counts
- **Unified API**: Single endpoint for multiple upstream Blossom servers
- **All Blossom Endpoints**: Supports upload, download, list, delete, mirror, and preflight checks
- **BUD-08 & NIP-94**: Returns proper tags with `nip94` array including URL tags and NIP-94 metadata
- **Streaming Uploads**: Uses streaming uploads to prevent authentication expiration on large files
- **Alternative Addresses**: Supports direct IP connections for upstream servers behind Cloudflare/proxies
- **Minimal Cache**: In-memory cache for hash-to-server mappings with configurable TTL and size limits
- **Thread-safe Operations**: Safe for concurrent requests
- **Web Dashboard**: Built-in home page with health status, statistics, memory, and goroutine monitoring
- **Docker Support**: Ready-to-use Dockerfile and docker-compose configurations (production and Cloudflare setups)

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

The project includes Docker support with docker-compose for easy deployment.

#### Prerequisites

- Docker and Docker Compose installed
- Configuration file created at `config/config.yaml` (copy from `config.example.yaml`)

#### Running with Docker Compose

1. **Create configuration file**:
   ```bash
   cp config/config.example.yaml config/config.yaml
   # Edit config/config.yaml with your upstream servers
   ```

2. **Choose and copy your docker-compose file**:
   
   For production setup (simple, direct access):
   ```bash
   cp docker-compose.prod.yml docker-compose.yml
   ```
   
   For Cloudflare setup (includes nginx and cloudflared):
   ```bash
   cp docker-compose.cloudflare.yml docker-compose.yml
   ```
   
   Then edit `docker-compose.yml` if needed (ports, environment variables, etc.)

3. **Start the services**:
   ```bash
   docker-compose up -d
   ```

   Production setup starts:
   - **nginx**: Reverse proxy (accessible on port `7624`)
   - **blossom-espelhator**: The proxy server (internal, accessed via nginx)
   - **autoheal**: Monitors containers and restarts them if unhealthy
   
   Cloudflare setup starts:
   - **blossom-espelhator**: The proxy server (internal, accessed via nginx)
   - **nginx**: Reverse proxy with Cloudflare-specific configuration
   - **cloudflared**: Cloudflare tunnel (requires `CLOUDFLARE_TUNNEL_TOKEN` in `config/.env`)
   - **autoheal**: Monitors containers and restarts if unhealthy

4. **View logs**:
   ```bash
   # All services
   docker-compose logs -f
   
   # Proxy only
   docker-compose logs -f blossom-espelhator
   ```

5. **Check status**:
   ```bash
   docker-compose ps
   ```

6. **Stop services**:
   ```bash
   docker-compose down
   ```

#### Docker Setup Features

- **Multi-stage build**: Optimized Alpine-based image for small size
- **Health checks**: Built-in health checks using `/health` endpoint
- **Autoheal**: Automatically restarts container if health checks fail
- **Verbose logging**: Runs with `-v` flag enabled by default for debugging
- **Project-specific labels**: Uses `blossom.espelhator.autoheal=true` to avoid conflicts
- **Read-only config mount**: Configuration file is mounted read-only for security
- **Non-root user**: Container runs as unprivileged `appuser`

#### Cloudflare Setup

The `docker-compose.cloudflare.yml` file provides a complete setup for deploying behind Cloudflare:

- **nginx**: Reverse proxy with Cloudflare-specific configuration (handles real IP headers, etc.)
- **cloudflared**: Cloudflare tunnel for secure connectivity
- **Environment**: Requires `CLOUDFLARE_TUNNEL_TOKEN` in `.env` file (in the current directory)

To use the Cloudflare setup:

1. Copy the Cloudflare compose file:
   ```bash
   cp docker-compose.cloudflare.yml docker-compose.yml
   ```

2. Copy and edit `.env` file in the current directory (where docker-compose.yml is):
   ```bash
   cp config/.env.example .env
   # Edit .env and set your CLOUDFLARE_TUNNEL_TOKEN
   ```
   
   Edit `.env` and set your `CLOUDFLARE_TUNNEL_TOKEN` value.

3. Start services:
   ```bash
   docker-compose up -d
   ```

4. The proxy is accessible via the Cloudflare tunnel URL configured in your Cloudflare dashboard.

#### Custom Port

The default host port is `7624` (maps to container port `8080`). To change it, edit `docker-compose.yml`:

```yaml
ports:
  - "YOUR_PORT:7624"  # Change YOUR_PORT to desired host port
```

#### Autoheal Configuration

The autoheal service monitors the proxy container and restarts it if it becomes unhealthy. To configure webhooks or adjust settings, edit the `autoheal` service in `docker-compose.yml`:

```yaml
autoheal:
  environment:
    - AUTOHEAL_INTERVAL=5           # Check interval in seconds
    - CURL_TIMEOUT=30               # Timeout for health checks
    # - WEBHOOK_URL=https://...     # Optional: webhook URL for events
```

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
  # Example: Server behind Cloudflare with direct IP access
  - url: "https://blossom3.example.com"
    alternative_address: "https://1.2.3.4"  # Direct IP or alternative hostname
    priority: 3
    supports_mirror: true
    supports_upload_head: true

# Proxy server configuration
server:
  listen_addr: ":8080"             # Address to listen on
  min_upload_servers: 2            # Minimum servers that must succeed for upload
  redirect_strategy: "round_robin" # Server selection strategy (see Redirect Strategies below)
  download_redirect_strategy: ""   # Optional: separate strategy for downloads (defaults to redirect_strategy)
  base_url: ""                     # Base URL for local strategy (optional, see Redirect Strategies)
  timeout: 30s                     # Timeout for download/HEAD/DELETE requests
  min_upload_timeout: 5m           # Minimum timeout for upload requests (default: 5 minutes)
  max_upload_timeout: 30m          # Maximum timeout for upload requests (default: 30 minutes)
  max_retries: 3                   # Maximum retries for failed requests
  
  # Health monitoring configuration
  max_failures: 5                  # Consecutive failures before marking server unhealthy
  
  # System resource limits for health checks
  max_goroutines: 1000             # Maximum allowed goroutines before marking system unhealthy
  max_memory_bytes: 536870912      # Maximum memory usage in bytes (512 MB) before marking system unhealthy
  
  # Cache configuration
  cache_ttl: 5m                    # Time-to-live for cache entries (default: 5 minutes)
  cache_max_size: 1000              # Maximum number of cache entries (default: 1000)
  
  # Authentication: List of allowed pubkeys (hex format or npub bech32 format)
  # If empty or not set, authentication is disabled
  # See Authentication Configuration section for details
  allowed_pubkeys: []
```

### Redirect Strategies

The `redirect_strategy` option controls how the proxy selects upstream servers for upload/mirror/list responses and downloads:

- **`round_robin`** (default): Cycles through available servers in order
- **`random`**: Randomly selects from available servers
- **`priority`**: Selects server with lowest priority number (lower is better). If multiple servers have the same priority, the first one found is selected
- **`health_based`**: Groups servers by total failures (sum of upload, mirror, delete, and list failures), then uses round-robin within the group with the lowest failures. Servers with more failures are excluded from selection
- **`local`**: Returns local URLs in response bodies (upload/mirror/list). Downloads still redirect to upstream servers using round-robin. Local URLs use format `base_url/sha256.ext` where:
  - `base_url` is from config if set, otherwise derived from request
  - Extension is derived from mime type or file extension, or omitted if unavailable

#### Download Redirect Strategy

The `download_redirect_strategy` option (optional) allows using a different strategy specifically for GET (download) requests:

- If not set or empty, falls back to `redirect_strategy`
- Useful when you want different behavior for downloads vs. uploads/mirrors/lists
- Example: Use `"priority"` for downloads while using `"health_based"` for uploads

```yaml
server:
  redirect_strategy: "health_based"      # For upload/mirror/list responses
  download_redirect_strategy: "priority" # For download redirects
```

### Base URL Configuration

The `base_url` option (optional) is used when `redirect_strategy` is `"local"`:

- If set, this URL is used for all local URL construction (e.g., `https://blossom.example.com`)
- If not set or empty, base URL is derived from the request (scheme + host)
- Useful for reverse proxy scenarios where you want to force a specific public URL
- Only affects response URLs when using `"local"` strategy, does not affect download redirects

### Server Configuration

- `url`: The upstream server URL (required) - used for building URLs in responses
- `alternative_address`: Optional alternative address for actual HTTP connections (bypasses Cloudflare/proxy limits)
  - If set, this address is used for all HTTP connections to the upstream server
  - The official `url` is still used when building URLs for responses
  - Useful when upstream servers are behind Cloudflare which limits payload size, but you know their real IP
  - Example: `"https://1.2.3.4"` or `"https://direct.example.com"`
- `priority`: Priority number for server selection when using `priority` strategy (lower is better, required)
- `supports_mirror`: If `true`, the server supports BUD-04 `/mirror` endpoint (optional, defaults to `false`)
- `supports_upload_head`: If `true`, the server supports BUD-06 `HEAD /upload` preflight checks (optional, defaults to `false`)

### Upload Timeout Configuration

Upload timeouts are calculated dynamically based on the authorization event's expiration timestamp:

- **Calculation**: Timeout = `expiration_timestamp - current_time - 30_seconds_buffer`
- **Clamping**: The calculated timeout is clamped between `min_upload_timeout` (minimum) and `max_upload_timeout` (maximum)
- **Default values**: 
  - `min_upload_timeout`: 5 minutes (prevents too-short timeouts)
  - `max_upload_timeout`: 30 minutes (prevents extremely long timeouts)
- **Purpose**: Prevents authentication expiration on large file uploads while avoiding excessive timeouts

If no expiration timestamp is provided in the authorization event, `min_upload_timeout` is used.

### Cache Configuration

The in-memory cache stores hash-to-server mappings to quickly determine which upstream servers have a blob:

- **`cache_ttl`**: Time-to-live for cache entries (default: 5 minutes)
  - Entries are automatically removed after this duration
  - Format: `"5m"`, `"10m"`, `"1h"`, etc.
- **`cache_max_size`**: Maximum number of entries in the cache (default: 1000)
  - When the cache reaches this size, least recently used (LRU) entries are evicted
  - Helps prevent unbounded memory growth

### Authentication Configuration

The `allowed_pubkeys` option enables authentication per [BUD-01](https://raw.githubusercontent.com/hzrd149/blossom/refs/heads/master/buds/01.md):

- **If `allowed_pubkeys` is empty or not set**: Authentication is disabled, all requests are allowed
- **If `allowed_pubkeys` contains pubkeys**: Only requests with valid Nostr authorization events from those pubkeys are accepted

Pubkeys can be specified in either format:
- **Hex format**: 64 hexadecimal characters (e.g., `b53185b9f27962ebdf76b8a9b0a84cd8b27f9f3d4abd59f715788a3bf9e7f75e`)
- **npub format**: bech32-encoded public key starting with `npub` (e.g., `npub1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx`)

Both formats are normalized to hex internally for comparison. Invalid pubkeys in the configuration are logged as warnings and skipped.

#### Authentication Requirements (BUD-01)

When `allowed_pubkeys` is configured, the following endpoints require authentication:
- `PUT /upload` - requires `t` tag with value `"upload"`
- `PUT /mirror` - requires `t` tag with value `"upload"` (uses upload event format)
- `DELETE /<sha256>` - requires `t` tag with value `"delete"`
- `GET /list/<pubkey>` - requires `t` tag with value `"list"`

Authorization events must:
1. Be kind `24242` (Blossom upload event format)
2. Have `created_at` in the past
3. Have `expiration` tag with future Unix timestamp
4. Have `t` tag matching the endpoint verb (`upload`, `delete`, `list`)
5. Have `pubkey` matching one in `allowed_pubkeys` (64 hex characters)
6. Be sent in `Authorization` header: `Authorization: Nostr <base64-encoded-event-json>`

Example configuration:
```yaml
server:
  allowed_pubkeys:
    - "b53185b9f27962ebdf76b8a9b0a84cd8b27f9f3d4abd59f715788a3bf9e7f75e"  # hex format
    - "npub1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"  # npub format
    - "ec0d11351457798907a3900fe465bfdc3b081be6efeb3d68c4d67774c0bc1f9a"  # hex format
```

Errors are returned with `X-Reason` header per BUD-01:
- `401 Unauthorized`: Missing or invalid authorization header/event
- `403 Forbidden`: Pubkey not in allowed list

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
  - Displays overall system health status
  - Shows healthy server count vs. minimum required
  - Displays memory usage (MB) vs. maximum limit with health indicator
  - Displays goroutine count vs. maximum limit with health indicator
  - Shows aggregated operation statistics (uploads, downloads, mirrors, deletes, lists)
  - Lists all upstream servers with per-server statistics and health status
  - Includes API documentation and usage examples

### Health & Statistics

- **GET /health** - Health check endpoint (returns JSON)
  - Returns `200 OK` if system is healthy (enough healthy servers, within memory/goroutine limits)
  - Returns `503 Service Unavailable` if system is unhealthy
  - Checks:
    - Server health: At least `min_upload_servers` upstream servers are healthy
    - Memory usage: Current memory usage is below `max_memory_bytes`
    - Goroutines: Current goroutine count is below `max_goroutines`
  - Response includes:
    - Overall health status
    - Healthy server count vs. minimum required
    - Memory usage (bytes) vs. maximum limit
    - Goroutine count vs. maximum limit
    - Per-server health status and consecutive failures

  Example response:
  ```json
  {
    "healthy": true,
    "healthy_count": 3,
    "min_upload_servers": 2,
    "memory": {
      "bytes": 16777216,
      "max": 536870912,
      "healthy": true
    },
    "goroutines": {
      "count": 42,
      "max": 1000,
      "healthy": true
    },
    "servers": {
      "https://server1.com": {
        "healthy": true,
        "consecutive_failures": 0
      }
    }
  }
  ```

- **GET /stats** - Statistics endpoint (returns JSON)
  - Aggregated statistics from all upstream servers
  - Per-server operation counts (uploads, downloads, mirrors, deletes, lists)
  - Success/failure counts and consecutive failures
  - System metrics: current memory usage and goroutine count
  - Last success/failure timestamps per server

### Blossom Protocol Endpoints

- **PUT /upload** - Upload a file (forwards to multiple upstream servers)
  - Requires Nostr authentication (kind 24242 event) if `allowed_pubkeys` is configured
  - Uses streaming uploads to prevent authentication expiration on large files
  - Upload timeout is calculated from authorization event's expiration timestamp (clamped between min/max)
  - Forwards to at least `min_upload_servers` upstream servers in parallel
  - Calculates SHA256 hash during upload (streaming) to avoid reading file twice
  - Returns response with `nip94` array containing URLs and metadata
  - If `redirect_strategy` is `"local"`, response URL uses local format (`base_url/sha256.ext`)

- **HEAD /upload** - Upload preflight check (BUD-06)
  - Headers: `X-SHA-256`, `X-Content-Length`, `X-Content-Type`
  - Checks if upstream servers would accept the upload
  - Authentication optional (not enforced by proxy)

- **PUT /mirror** - Mirror a blob (BUD-04)
  - Request body: `{"url": "<blob-url>"}`
  - Requires Nostr authentication (kind 24242 event) if `allowed_pubkeys` is configured
  - Only forwards to servers with `supports_mirror: true`
  - Returns response with `nip94` array
  - If `redirect_strategy` is `"local"`, response URL uses local format (`base_url/sha256.ext`)

- **GET /list/<pubkey>** - List files for a pubkey
  - Requires Nostr authentication (kind 24242 event) if `allowed_pubkeys` is configured
  - Queries all upstream servers in parallel
  - Merges and deduplicates results based on `sha256`
  - Returns list with `nip94` tags for each item
  - If `redirect_strategy` is `"local"`, item URLs use local format (`base_url/sha256.ext`)

- **GET /<sha256>.<ext>** - Download file
  - Redirects to one of the upstream servers that has the file
  - Uses `download_redirect_strategy` if configured, otherwise falls back to `redirect_strategy`
  - Available strategies: round_robin, random, priority, health_based, or local (uses round-robin for downloads)
  - Authentication optional (not enforced by proxy, may be required by upstream servers)

- **HEAD /<sha256>.<ext>** - Check file existence
  - Proxies HEAD request to upstream server
  - Returns headers and status code from upstream
  - Authentication optional (not enforced by proxy, may be required by upstream servers)

- **DELETE /<sha256>** - Delete file
  - Requires Nostr authentication (kind 24242 event) if `allowed_pubkeys` is configured
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

The proxy tracks health at multiple levels:

### Upstream Server Health

- **Consecutive Failures**: Counts consecutive operation failures per server
- **Unhealthy Threshold**: Server marked unhealthy when failures exceed `max_failures` (default: 5)
- **Auto Recovery**: Failures reset to 0 on successful operation
- **Startup State**: All servers start as healthy and only become unhealthy after failures

### System Health

The system is considered healthy when **all** of the following conditions are met:

1. **Server Health**: At least `min_upload_servers` upstream servers are healthy
2. **Memory Usage**: Current memory allocation is below `max_memory_bytes` (default: 512 MB)
3. **Goroutines**: Current goroutine count is below `max_goroutines` (default: 1000)

The `/health` endpoint checks all three conditions and returns `200 OK` only if all pass. If any check fails, it returns `503 Service Unavailable`.

### Monitoring

- **Homepage**: Displays memory and goroutine usage with health indicators
- **Health Endpoint**: JSON response includes memory/goroutine metrics and health status
- **Stats Endpoint**: Includes current memory and goroutine counts in the response

## Statistics

The `/stats` endpoint provides comprehensive statistics:

- **Per-server statistics**:
  - Operation counts (uploads, downloads, mirrors, deletes, lists)
  - Success/failure counts for each operation type
  - Consecutive failures
  - Health status
  - Last success/failure timestamps

- **Aggregated totals**: Sum of all operations across all servers

- **System metrics**:
  - Current memory usage (bytes) and maximum limit
  - Current goroutine count and maximum limit

Example response:
```json
{
  "servers": {
    "https://server1.com": {
      "url": "https://server1.com",
      "uploads_success": 150,
      "uploads_failure": 2,
      "downloads": 1250,
      "mirrors_success": 10,
      "mirrors_failure": 0,
      "deletes_success": 5,
      "deletes_failure": 0,
      "lists_success": 200,
      "lists_failure": 1,
      "consecutive_failures": 0,
      "is_healthy": true
    }
  },
  "totals": {
    "uploads_success": 450,
    "uploads_failure": 5,
    "downloads": 3750,
    "mirrors_success": 30,
    "mirrors_failure": 0,
    "deletes_success": 15,
    "deletes_failure": 0,
    "lists_success": 600,
    "lists_failure": 3
  },
  "memory": {
    "bytes": 25165824,
    "max": 536870912
  },
  "goroutines": {
    "count": 45,
    "max": 1000
  }
}
```

## Usage with Nostr Clients

Configure your Nostr client to use this proxy server:

1. Set Blossom server URL to your proxy address (e.g., `http://blossom.example.com`)
2. Files uploaded through your client will be forwarded to multiple upstream servers
3. Downloads automatically use the best available upstream server

### Helper Scripts

The `scripts/` directory includes helper scripts (see `scripts/README.md` for detailed documentation):

- `gen_auth_header.sh` - Generate Nostr authentication headers for API requests (NIP-98 HTTP Schnorr Auth)
  - Requires `nak` (Nostr CLI tool) and `jq`
  - Generates `Authorization: Nostr <base64>` headers for authenticated requests
  - Supports all Blossom endpoints (upload, mirror, delete, list)
- `upload_file.sh` - Upload files with proper authentication
- `mirror_hash.sh` - Mirror blobs using the mirror endpoint
- `list_pubkey.sh` - List blobs for a pubkey with authentication

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
