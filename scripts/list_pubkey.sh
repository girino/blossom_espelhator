#!/usr/bin/env bash
# One-liner helper: List blobs for a given public key using curl with proper auth
# Usage: ./scripts/list_pubkey.sh <pubkey> [server_url]
# Example: ./scripts/list_pubkey.sh npub18lav8fkgt8424rxamvk8qq4xuy9n8mltjtgztv2w44hc5tt9vets0hcfsz
# Example: ./scripts/list_pubkey.sh b53185b9f27962ebdf76b8a9b0a84cd8b27f9f3d4abd59f715788a3bf9e7f75e

PUBKEY="${1:-}"
SERVER_URL="${2:-http://localhost:7624}"

if [ -z "$PUBKEY" ]; then
    echo "Usage: $0 <pubkey> [server_url]"
    echo "Example: $0 npub18lav8fkgt8424rxamvk8qq4xuy9n8mltjtgztv2w44hc5tt9vets0hcfsz"
    echo "Example: $0 b53185b9f27962ebdf76b8a9b0a84cd8b27f9f3d4abd59f715788a3bf9e7f75e"
    exit 1
fi

# Check for required tools
if ! command -v nak &> /dev/null; then
    echo "Error: 'nak' command not found. Install it from https://github.com/fiatjaf/nak"
    exit 1
fi

if ! command -v jq &> /dev/null; then
    echo "Error: 'jq' command not found. Install it: apt-get install jq"
    exit 1
fi

# Convert pubkey to hex format if it's npub format
HEX_PUBKEY=""
if echo "$PUBKEY" | grep -qE '^npub1'; then
    # Decode npub to hex
    HEX_PUBKEY=$(nak decode "$PUBKEY" 2>/dev/null | tr -d '[:space:]')
    if [ -z "$HEX_PUBKEY" ] || [ "${#HEX_PUBKEY}" -ne 64 ]; then
        echo "Error: Failed to decode npub to hex format"
        exit 1
    fi
else
    # Assume it's already hex format (validate it)
    HEX_PUBKEY=$(echo "$PUBKEY" | tr -d '[:space:]')
    if ! echo "$HEX_PUBKEY" | grep -qE '^[0-9a-fA-F]{64}$'; then
        echo "Error: Invalid pubkey format. Must be npub1... or 64 hex characters"
        exit 1
    fi
    # Normalize to lowercase
    HEX_PUBKEY=$(echo "$HEX_PUBKEY" | tr '[:upper:]' '[:lower:]')
fi

LIST_URL="$SERVER_URL/list/$HEX_PUBKEY"

# Generate auth header
# gen_auth_header.sh outputs debug to stderr and the header to stdout
HEADER=$(./scripts/gen_auth_header.sh GET "$LIST_URL" 2>/dev/null)
GEN_HEADER_EXIT=$?

if [ $GEN_HEADER_EXIT -ne 0 ] || [ -z "$HEADER" ]; then
    echo "Error: Failed to generate authentication header." >&2
    exit 1
fi

# Print header for debug
echo "[DEBUG] Authorization header: $HEADER" >&2

# List blobs and pretty print the response
# Use -L to follow redirects, -w to append HTTP status code, -S to show errors even with -s
RESPONSE=$(curl -sSL -X GET -H "$HEADER" -w "\n%{http_code}" "$LIST_URL" 2>&1)
CURL_EXIT=$?

if [ $CURL_EXIT -ne 0 ]; then
    echo "Error: Request failed (connection error)" >&2
    echo "$RESPONSE" >&2
    exit 1
fi

# Split response body and HTTP status code
HTTP_BODY=$(echo "$RESPONSE" | head -n -1)
HTTP_CODE=$(echo "$RESPONSE" | tail -n 1)

if [ "$HTTP_CODE" != "200" ]; then
    echo "Error: HTTP $HTTP_CODE" >&2
    echo "$HTTP_BODY" | jq '.' 2>/dev/null || echo "$HTTP_BODY" >&2
    exit 1
fi

# Pretty print the JSON response
echo "$HTTP_BODY" | jq '.'
