#!/usr/bin/env bash
# One-liner helper: Upload a file using curl with proper auth
# Usage: ./scripts/upload_file.sh <file_path> [server_url]
# Example: ./scripts/upload_file.sh bitcoin.pdf
# Example: ./scripts/upload_file.sh image.png http://localhost:7624

FILE_PATH="${1:-}"
SERVER_URL="${2:-http://localhost:7624}"

if [ -z "$FILE_PATH" ]; then
    echo "Usage: $0 <file_path> [server_url]"
    echo "Example: $0 bitcoin.pdf"
    echo "Example: $0 image.png http://localhost:7624"
    exit 1
fi

if [ ! -f "$FILE_PATH" ]; then
    echo "Error: File not found: $FILE_PATH"
    exit 1
fi

# Get the filename from the path
FILE_NAME=$(basename "$FILE_PATH")

# Detect Content-Type based on file extension
CONTENT_TYPE="application/octet-stream"
case "${FILE_PATH##*.}" in
    pdf)
        CONTENT_TYPE="application/pdf"
        ;;
    png)
        CONTENT_TYPE="image/png"
        ;;
    jpg|jpeg)
        CONTENT_TYPE="image/jpeg"
        ;;
    gif)
        CONTENT_TYPE="image/gif"
        ;;
    webp)
        CONTENT_TYPE="image/webp"
        ;;
    mp4)
        CONTENT_TYPE="video/mp4"
        ;;
    mp3)
        CONTENT_TYPE="audio/mpeg"
        ;;
    txt)
        CONTENT_TYPE="text/plain"
        ;;
    json)
        CONTENT_TYPE="application/json"
        ;;
    *)
        # Try to detect using file command if available
        if command -v file &> /dev/null; then
            FILE_OUTPUT=$(file -b --mime-type "$FILE_PATH" 2>/dev/null || echo "")
            if [ -n "$FILE_OUTPUT" ]; then
                CONTENT_TYPE="$FILE_OUTPUT"
            fi
        fi
        ;;
esac

UPLOAD_URL="$SERVER_URL/upload"

# Generate auth header
HEADER=$(./scripts/gen_auth_header.sh PUT "$UPLOAD_URL" "$FILE_PATH" "$FILE_NAME")

# Upload the file and pretty print the response
curl -s -X PUT -H "$HEADER" -H "Content-Type: $CONTENT_TYPE" --data-binary @"$FILE_PATH" "$UPLOAD_URL" | jq '.'
