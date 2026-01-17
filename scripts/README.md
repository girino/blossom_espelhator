# Blossom Espelhator Helper Scripts

## gen_auth_header.sh

Generates a Nostr Authorization header for authenticated Blossom server requests (NIP-98 HTTP Schnorr Auth).

### Requirements

- `nak` - Nostr CLI tool (install from https://github.com/fiatjaf/nak)
- `jq` - JSON processor (`apt-get install jq` or `brew install jq`)
- `bash` - Bash shell

### Setup

Set your Nostr private key as an environment variable:

```bash
# Generate a new key (recommended)
HEX_KEY=$(nak key generate | tr -d '[:space:]')
export NOSTR_SECRET_KEY=$(echo "$HEX_KEY" | nak encode nsec | tr -d '[:space:]')

# Or set manually
export NOSTR_SECRET_KEY="nsec1..."
# or hex format (64 hex characters)
export NOSTR_SECRET_KEY="abcd1234..."
```

**Note:** Make sure there are no extra characters or whitespace in the key. The script will validate the format automatically.

### Usage

```bash
./scripts/gen_auth_header.sh METHOD URL [BODY_FILE]
```

### Examples

Generate auth header for HEAD /upload (no body):

```bash
./scripts/gen_auth_header.sh HEAD http://localhost:8080/upload
```

Generate auth header for PUT /upload with body file:

```bash
# Create a test body file
echo '{"test":"data"}' > body.json

# Generate header
./scripts/gen_auth_header.sh PUT http://localhost:8080/upload body.json
```

Use with curl:

```bash
# For HEAD request
HEADER=$(./scripts/gen_auth_header.sh HEAD http://localhost:8080/upload)
curl -I -H "$HEADER" \
     -H "X-SHA-256: a49944fa9c909d7c2a2ac50bbdb9e3d39ba08347d611dbabc0ba426f33b2d9da" \
     -H "X-Content-Length: 2280488" \
     -H "X-Content-Type: image/png" \
     http://localhost:8080/upload

# For PUT /mirror
echo '{"hash":"a49944fa9c909d7c2a2ac50bbdb9e3d39ba08347d611dbabc0ba426f33b2d9da"}' > mirror.json
HEADER=$(./scripts/gen_auth_header.sh PUT http://localhost:8080/mirror mirror.json)
curl -X PUT -H "$HEADER" \
     -H "Content-Type: application/json" \
     -d @mirror.json \
     http://localhost:8080/mirror
```

### How it works

1. Creates a Nostr event with kind 27235 (HTTP Auth per NIP-98)
2. Adds tags for HTTP method and URL
3. If a body file is provided, computes SHA256 hash and adds as `payload` tag
4. Signs the event with your private key using `nak`
5. Base64-encodes the signed event
6. Outputs `Authorization: Nostr <base64>` header

The generated header can be used directly with `curl -H` or saved to a variable for reuse.
