#!/usr/bin/env bash
#
# Generate Nostr Authorization header for Blossom server requests
# Always uses Blossom upload event format (kind 24242)
#
# Usage:
#   ./scripts/gen_auth_header.sh [-h|--hash <hash>] METHOD URL [BODY_FILE] [FILE_NAME]
#
# Examples:
#   ./scripts/gen_auth_header.sh PUT http://localhost:8080/upload /path/to/file.pdf "bitcoin.pdf"
#   ./scripts/gen_auth_header.sh PUT http://localhost:8080/mirror mirror.json
#   ./scripts/gen_auth_header.sh --hash abc123... PUT http://localhost:8080/mirror mirror.json
#   ./scripts/gen_auth_header.sh HEAD http://localhost:8080/upload
#
# Environment:
#   NOSTR_SECRET_KEY - Your private key (nsec1... or hex format)
#   BLOSSOM_EXPIRATION - Expiration timestamp (optional, defaults to 24 hours from now)

set -euo pipefail

BLOB_HASH=""

# Parse optional flags
while [ "$#" -gt 0 ]; do
    case "$1" in
        -h|--hash)
            if [ -z "${2:-}" ]; then
                echo "Error: -h/--hash requires a hash value" >&2
                echo "Usage: $0 [-h|--hash <hash>] METHOD URL [BODY_FILE] [FILE_NAME]" >&2
                exit 1
            fi
            BLOB_HASH="$2"
            shift 2
            ;;
        --)
            shift
            break
            ;;
        -*)
            echo "Error: Unknown option: $1" >&2
            echo "Usage: $0 [-h|--hash <hash>] METHOD URL [BODY_FILE] [FILE_NAME]" >&2
            exit 1
            ;;
        *)
            break
            ;;
    esac
done

METHOD="${1:-}"
URL="${2:-}"
BODY_FILE="${3:-}"
FILE_NAME="${4:-}"

if [ -z "$METHOD" ] || [ -z "$URL" ]; then
    echo "Usage: $0 [-h|--hash <hash>] METHOD URL [BODY_FILE] [FILE_NAME]"
    echo ""
    echo "Examples:"
    echo "  $0 HEAD http://localhost:8080/upload"
    echo "  $0 PUT http://localhost:8080/upload body.json"
    echo "  $0 --hash abc123... PUT http://localhost:8080/mirror mirror.json"
    echo ""
    echo "Set NOSTR_SECRET_KEY environment variable with your private key"
    exit 1
fi

# Check for private key
if [ -z "${NOSTR_SECRET_KEY:-}" ]; then
    echo "Error: NOSTR_SECRET_KEY environment variable is not set"
    echo "Example: export NOSTR_SECRET_KEY=\"nsec1...\""
    exit 1
fi

# Remove any leading/trailing whitespace from the key
NOSTR_SECRET_KEY=$(echo "$NOSTR_SECRET_KEY" | xargs)

# Validate key format (should start with nsec1, ncryptsec1, or be hex)
if ! echo "$NOSTR_SECRET_KEY" | grep -qE '^nsec1|^ncryptsec1|^[0-9a-fA-F]{64}$'; then
    echo "Error: Invalid NOSTR_SECRET_KEY format"
    echo "Key should be: nsec1..., ncryptsec1..., or 64 hex characters"
    echo "Your key starts with: $(echo "$NOSTR_SECRET_KEY" | cut -c1-10)..."
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

# Get current timestamp
CREATED_AT=$(date +%s)

# Always use Blossom upload event format (kind 24242) for all requests
echo "[DEBUG] Using Blossom upload event format (kind 24242)" >&2
KIND=24242

# Action tag "t" must match the endpoint per BUD-01: list -> "list", upload/mirror -> "upload", delete -> "delete"
if echo "$URL" | grep -qE '/list[/?]|/list$'; then
    ACTION_TAG="list"
elif echo "$URL" | grep -qE '/mirror[/?]|/mirror$'; then
    ACTION_TAG="upload"
elif echo "$URL" | grep -qE '/upload[/?]|/upload$'; then
    ACTION_TAG="upload"
elif echo "$URL" | grep -qE '/delete[/?]|/delete$'; then
    ACTION_TAG="delete"
else
    # Default to upload for unknown paths (e.g. GET /list is the main one that needs "list")
    ACTION_TAG="upload"
fi
echo "[DEBUG] Action tag for URL: [\"t\",\"$ACTION_TAG\"]" >&2

# Build tags for Blossom event: ["t", "<action>"], ["x", "hash"], ["expiration", "timestamp"]
TAGS=("[\"t\",\"$ACTION_TAG\"]")

# Determine the hash - priority: -h/--hash parameter > extract from JSON body URL > compute from file
# Hash parameter is passed by mirror_hash.sh to use the hash from the blob URL, not the JSON file
# For other scripts (upload_file.sh, etc.), hash is computed from the body file as before
HASH=""
if [ -n "$BLOB_HASH" ]; then
    # Hash explicitly passed via command-line parameter (e.g. from mirror_hash.sh)
    # Validate hash format
    if ! echo "$BLOB_HASH" | grep -qE '^[0-9a-fA-F]{64}$'; then
        echo "Error: Invalid hash format: $BLOB_HASH" >&2
        echo "Hash must be 64 hexadecimal characters." >&2
        exit 1
    fi
    HASH="$BLOB_HASH"
    echo "[DEBUG] Using hash from command-line parameter: $HASH" >&2
elif [ -n "$BODY_FILE" ] && [ -f "$BODY_FILE" ]; then
    if echo "$URL" | grep -q "/mirror"; then
        # For mirror requests with JSON body, try to extract hash from the JSON's URL field
        # This allows other scripts calling gen_auth_header.sh with mirror JSON to work correctly
        BODY_URL=$(jq -r '.url // empty' "$BODY_FILE" 2>/dev/null)
        if [ -n "$BODY_URL" ]; then
            # Extract hash from URL (last path component, remove extension)
            HASH=$(echo "$BODY_URL" | sed 's|.*/||' | sed 's/\.[^.]*$//')
            if echo "$HASH" | grep -qE '^[0-9a-fA-F]{64}$'; then
                echo "[DEBUG] Extracted hash from JSON body URL field: $HASH" >&2
            else
                HASH=""
            fi
        fi
        # Fallback: compute hash from JSON file if extraction failed
        if [ -z "$HASH" ]; then
            HASH=$(sha256sum "$BODY_FILE" | awk '{print $1}')
            echo "[DEBUG] Computed hash from body file (fallback for mirror): $HASH" >&2
        fi
    else
        # For non-mirror requests (e.g. /upload), always compute hash from body file
        # This preserves backward compatibility for upload_file.sh and other scripts
        HASH=$(sha256sum "$BODY_FILE" | awk '{print $1}')
        echo "[DEBUG] Computed hash from body file: $HASH" >&2
    fi
elif echo "$URL" | grep -q "/mirror"; then
    # For mirror requests without body file, hash will be in request body
    # This case is unlikely but preserved for backward compatibility
    echo "[DEBUG] Mirror request without body file - hash will be in request body" >&2
fi

# If we have a hash, add it as ["x", "hash"] tag
if [ -n "$HASH" ]; then
    TAGS+=("[\"x\",\"$HASH\"]")
fi

# Set expiration (default: 24 hours from now)
EXPIRATION="${BLOSSOM_EXPIRATION:-$((CREATED_AT + 86400))}"
TAGS+=("[\"expiration\",\"$EXPIRATION\"]")

# Content should describe the action
if [ -n "$FILE_NAME" ]; then
    CONTENT="Upload $FILE_NAME"
elif echo "$URL" | grep -q "/mirror"; then
    CONTENT="Mirror blob"
elif echo "$URL" | grep -q "/upload"; then
    if [ -n "$BODY_FILE" ]; then
        CONTENT="Upload $(basename "$BODY_FILE" 2>/dev/null || echo "file")"
    else
        CONTENT="Upload"
    fi
else
    CONTENT="Blossom request"
fi

# Build tags JSON array
TAGS_JSON=$(printf '[%s]' "$(IFS=,; echo "${TAGS[*]}")")

# Build the event JSON (without signature first)
# We'll let nak add the pubkey when signing, or decode nsec if needed
# First, try to get pubkey - decode nsec to hex first if needed
if echo "$NOSTR_SECRET_KEY" | grep -qE '^nsec1'; then
    # Decode nsec to hex, then get pubkey
    HEX_KEY=$(nak decode "$NOSTR_SECRET_KEY" 2>/dev/null | tr -d '[:space:]')
    if [ -n "$HEX_KEY" ] && [ "${#HEX_KEY}" -eq 64 ]; then
        PUBKEY=$(nak key public "$HEX_KEY" 2>/dev/null | tr -d '[:space:]')
    else
        PUBKEY=""
    fi
else
    # Already hex format, try to get pubkey directly
    PUBKEY=$(nak key public "$NOSTR_SECRET_KEY" 2>/dev/null | tr -d '[:space:]')
fi

# If we couldn't get pubkey (or nak doesn't support it), let nak add it when signing
if [ -z "$PUBKEY" ] || [ "${#PUBKEY}" -ne 64 ]; then
    # Build event without pubkey, nak will add it when signing
    EVENT_JSON=$(jq -nc \
        --arg created_at "$CREATED_AT" \
        --arg kind "$KIND" \
        --arg content "$CONTENT" \
        --argjson tags "$TAGS_JSON" \
        '{
            "created_at": ($created_at | tonumber),
            "kind": ($kind | tonumber),
            "tags": $tags,
            "content": $content
        }')
else
    # Build event with pubkey
    EVENT_JSON=$(jq -nc \
        --arg pubkey "$PUBKEY" \
        --arg created_at "$CREATED_AT" \
        --arg kind "$KIND" \
        --arg content "$CONTENT" \
        --argjson tags "$TAGS_JSON" \
        '{
            "pubkey": $pubkey,
            "created_at": ($created_at | tonumber),
            "kind": ($kind | tonumber),
            "tags": $tags,
            "content": $content
        }')
fi

# Sign the event using nak (pipe JSON directly, nak will hash and sign it)
SIGNED_EVENT=$(echo "$EVENT_JSON" | nak event --sec "$NOSTR_SECRET_KEY" 2>&1)

# Check if signing failed
if echo "$SIGNED_EVENT" | grep -q "invalid\|error\|Error"; then
    echo "Error: Failed to sign event with nak:" >&2
    echo "$SIGNED_EVENT" >&2
    exit 1
fi

# Debug: Print the signed event JSON
echo "[DEBUG] Signed event:" >&2
echo "$SIGNED_EVENT" | jq . >&2

# Base64 encode the signed event (compact JSON)
# Handle different base64 implementations (some use -w 0, others don't support it)
SIGNED_EVENT_COMPACT=$(echo "$SIGNED_EVENT" | jq -c .)
if base64 --help 2>&1 | grep -q "\-w"; then
    AUTH_EVENT_BASE64=$(echo "$SIGNED_EVENT_COMPACT" | base64 -w 0)
else
    AUTH_EVENT_BASE64=$(echo "$SIGNED_EVENT_COMPACT" | base64 | tr -d '\n')
fi

# Debug: Print the base64 token
echo "[DEBUG] Base64 token:" >&2
echo "$AUTH_EVENT_BASE64" >&2

# Output the Authorization header
echo "Authorization: Nostr $AUTH_EVENT_BASE64"
