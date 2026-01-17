#!/usr/bin/env bash
# One-liner helper: Mirror a blob using curl with proper auth
# Usage: ./scripts/mirror_hash.sh <url>
# Example: ./scripts/mirror_hash.sh https://server.com/a49944fa9c909d7c2a2ac50bbdb9e3d39ba08347d611dbabc0ba426f33b2d9da

URL="${1:-}"
if [ -z "$URL" ]; then
    echo "Usage: $0 <url>"
    echo "Example: $0 https://server.com/a49944fa9c909d7c2a2ac50bbdb9e3d39ba08347d611dbabc0ba426f33b2d9da"
    exit 1
fi

TMPFILE=$(mktemp)
echo "{\"url\":\"$URL\"}" > "$TMPFILE"
HEADER=$(./scripts/gen_auth_header.sh PUT "http://localhost:8080/mirror" "$TMPFILE")
curl -X PUT -H "$HEADER" -H "Content-Type: application/json" -d @"$TMPFILE" http://localhost:8080/mirror
echo ""
rm -f "$TMPFILE"
