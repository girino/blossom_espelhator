#!/usr/bin/env bash
# One-liner helper: Mirror a blob using curl with proper auth
# Usage: ./scripts/mirror_hash.sh <url> [server_url]
# Example: ./scripts/mirror_hash.sh https://server.com/a49944fa9c909d7c2a2ac50bbdb9e3d39ba08347d611dbabc0ba426f33b2d9da
# Example: ./scripts/mirror_hash.sh https://server.com/a49944fa9c909d7c2a2ac50bbdb9e3d39ba08347d611dbabc0ba426f33b2d9da http://localhost:7624

URL="${1:-}"
SERVER_URL="${2:-http://localhost:7624}"

# Remove trailing slash from server URL if present
#SERVER_URL="${SERVER_URL%/}"

if [ -z "$URL" ]; then
    echo "Usage: $0 <url> [server_url]" >&2
    echo "Example: $0 https://server.com/a49944fa9c909d7c2a2ac50bbdb9e3d39ba08347d611dbabc0ba426f33b2d9da" >&2
    echo "Example: $0 https://server.com/a49944fa9c909d7c2a2ac50bbdb9e3d39ba08347d611dbabc0ba426f33b2d9da http://localhost:7624" >&2
    exit 1
fi

# Check for required tools
if ! command -v curl &> /dev/null; then
    echo "Error: 'curl' command not found. Install it: apt-get install curl" >&2
    exit 1
fi

if ! command -v jq &> /dev/null; then
    echo "Error: 'jq' command not found. Install it: apt-get install jq" >&2
    exit 1
fi

# Create temporary file
TMPFILE=$(mktemp)
if [ $? -ne 0 ]; then
    echo "Error: Failed to create temporary file" >&2
    exit 1
fi

# Clean up temp file on exit
trap "rm -f \"$TMPFILE\"" EXIT

echo "{\"url\":\"$URL\"}" > "$TMPFILE"
if [ $? -ne 0 ]; then
    echo "Error: Failed to write to temporary file" >&2
    exit 1
fi

# Generate auth header
# gen_auth_header.sh outputs debug to stderr and the header to stdout
HEADER=$(./scripts/gen_auth_header.sh PUT "$SERVER_URL/mirror" "$TMPFILE" 2>/dev/null)
GEN_HEADER_EXIT=$?

if [ $GEN_HEADER_EXIT -ne 0 ] || [ -z "$HEADER" ]; then
    echo "Error: Failed to generate authentication header." >&2
    exit 1
fi

# Print header for debug
echo "[DEBUG] Authorization header: $HEADER" >&2

# Debug: show JSON payload
if [ -n "$DEBUG" ]; then
    echo "[DEBUG] JSON payload:" >&2
    cat "$TMPFILE" >&2
    echo >&2
fi

# Perform the curl request and capture output and HTTP status code
MIRROR_URL="$SERVER_URL/mirror"
CURL_OUTPUT=$(curl -sSL -X PUT -H "$HEADER" -H "Content-Type: application/json" -d @"$TMPFILE" -w "\n%{http_code}" "$MIRROR_URL" 2>&1)
CURL_EXIT=$?

if [ $CURL_EXIT -ne 0 ]; then
    echo "Error: Request failed (connection error)" >&2
    echo "$CURL_OUTPUT" >&2
    exit 1
fi

# Split response body and HTTP status code
HTTP_BODY=$(echo "$CURL_OUTPUT" | head -n -1)
HTTP_CODE=$(echo "$CURL_OUTPUT" | tail -n 1)

if [ "$HTTP_CODE" != "200" ] && [ "$HTTP_CODE" != "201" ] && [ "$HTTP_CODE" != "202" ]; then
    echo "Error: HTTP $HTTP_CODE" >&2
    echo "$HTTP_BODY" | jq '.' 2>/dev/null || echo "$HTTP_BODY" >&2
    exit 1
fi

# Pretty print the successful JSON response
echo "$HTTP_BODY" | jq '.'
