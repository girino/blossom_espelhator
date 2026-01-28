#!/usr/bin/env bash
# One-liner helper: Mirror a blob using curl with proper auth
# Usage: ./scripts/mirror_hash.sh [-v|--verbose] <url> [server_url]
# Example: ./scripts/mirror_hash.sh https://server.com/a49944fa9c909d7c2a2ac50bbdb9e3d39ba08347d611dbabc0ba426f33b2d9da
# Example: ./scripts/mirror_hash.sh -v https://server.com/a49944fa9c909d7c2a2ac50bbdb9e3d39ba08347d611dbabc0ba426f33b2d9da http://localhost:7624

VERBOSE=""

verb() {
    [ -n "$VERBOSE" ] && echo "[DEBUG] $*" >&2
}

# Parse optional flags
while [ "$#" -gt 0 ]; do
    case "$1" in
        -v|--verbose)
            VERBOSE=1
            shift
            ;;
        --)
            shift
            break
            ;;
        -*)
            echo "Error: Unknown option: $1" >&2
            echo "Usage: $0 [-v|--verbose] <url> [server_url]" >&2
            exit 1
            ;;
        *)
            break
            ;;
    esac
done

URL="${1:-}"
SERVER_URL="${2:-http://localhost:7624}"

# Remove trailing slash from server URL if present
#SERVER_URL="${SERVER_URL%/}"

if [ -z "$URL" ]; then
    echo "Usage: $0 [-v|--verbose] <url> [server_url]" >&2
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

# Extract hash from URL (last path component, remove extension if present)
# URL format: https://server.com/hash or https://server.com/hash.ext
BLOB_HASH=$(echo "$URL" | sed 's|.*/||' | sed 's/\.[^.]*$//')
# Validate it looks like a hash (64 hex chars)
if ! echo "$BLOB_HASH" | grep -qE '^[0-9a-fA-F]{64}$'; then
    echo "Error: Could not extract valid hash from URL: $URL" >&2
    echo "Expected format: https://server.com/<64-hex-char-hash> or https://server.com/<hash>.<ext>" >&2
    exit 1
fi
verb "Extracted hash from URL: $BLOB_HASH"

echo "{\"url\":\"$URL\"}" > "$TMPFILE"
if [ $? -ne 0 ]; then
    echo "Error: Failed to write to temporary file" >&2
    exit 1
fi

# Generate auth header
# gen_auth_header.sh outputs debug to stderr and the header to stdout
# Pass hash via environment variable so gen_auth_header can use it (extracted from URL, not JSON file)
verb "Generating auth header with gen_auth_header.sh PUT $SERVER_URL/mirror $TMPFILE (hash=$BLOB_HASH)"
export BLOB_HASH
HEADER=$(./scripts/gen_auth_header.sh PUT "$SERVER_URL/mirror" "$TMPFILE" 2>/dev/null)
GEN_HEADER_EXIT=$?
unset BLOB_HASH

if [ $GEN_HEADER_EXIT -ne 0 ] || [ -z "$HEADER" ]; then
    echo "Error: Failed to generate authentication header." >&2
    verb "gen_auth_header.sh exit=$GEN_HEADER_EXIT header='$HEADER'"
    exit 1
fi

# Print header for debug
verb "Authorization header: $HEADER"

# Debug: show JSON payload
if [ -n "$VERBOSE" ] || [ -n "$DEBUG" ]; then
    echo "[DEBUG] JSON payload:" >&2
    cat "$TMPFILE" >&2
    echo >&2
fi

# Perform the curl request and capture output with headers and HTTP status code
MIRROR_URL="$SERVER_URL/mirror"
verb "Calling curl: curl -i -sSL -X PUT -H \"<auth>\" -H \"Content-Type: application/json\" -d @\"$TMPFILE\" -w '\\n%{http_code}' \"$MIRROR_URL\""
CURL_OUTPUT=$(curl -i -sSL -X PUT -H "$HEADER" -H "Content-Type: application/json" -d @"$TMPFILE" -w "\n%{http_code}" "$MIRROR_URL" 2>&1)
CURL_EXIT=$?

if [ $CURL_EXIT -ne 0 ]; then
    echo "Error: Request failed (connection error)" >&2
    [ -n "$VERBOSE" ] && echo "$CURL_OUTPUT" >&2
    exit 1
fi

# Split response: headers + body, then HTTP status code
# Format with -i: headers\n\nbody\nHTTP_CODE
HTTP_CODE=$(echo "$CURL_OUTPUT" | tail -n 1)
RESPONSE_WITHOUT_CODE=$(echo "$CURL_OUTPUT" | head -n -1)

# Split headers and body (headers end with blank line, then body)
# Use awk to find the blank line separator
HEADERS=$(echo "$RESPONSE_WITHOUT_CODE" | awk 'BEGIN{found=0} /^$/{found=1; next} found==0{print}')
HTTP_BODY=$(echo "$RESPONSE_WITHOUT_CODE" | awk 'BEGIN{found=0} /^$/{found=1; next} found==1{print}')

verb "HTTP_CODE=$HTTP_CODE"

if [ "$HTTP_CODE" != "200" ] && [ "$HTTP_CODE" != "201" ] && [ "$HTTP_CODE" != "202" ]; then
    echo "Error: HTTP $HTTP_CODE" >&2
    echo "Response headers:" >&2
    echo "$HEADERS" >&2
    echo "" >&2
    echo "Response body:" >&2
    echo "$HTTP_BODY" | jq '.' 2>/dev/null || echo "$HTTP_BODY" >&2
    exit 1
fi

verb "Mirror successful, printing response JSON"
# Pretty print the successful JSON response
echo "$HTTP_BODY" | jq '.'
