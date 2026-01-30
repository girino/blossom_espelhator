#!/usr/bin/env bash
#
# Equalize a server by mirroring blobs listed in a list_missing_blobs output file.
# Calls PUT /mirror on the target server for each hash/url in the file, using
# NOSTR_SECRET_KEY for auth. Failed mirrors are written to an errors file.
#
# Usage: ./scripts/equalize_server.sh <server_url> <list_missing_blobs_output_file> [-o base] [-v] [-n]
#
# Arguments:
#   server_url   Target server to equalize (e.g. https://blossom.example.com)
#   output_file  File produced by list_missing_blobs -o (lines: hash source_url inferred_url)
#
# Options:
#   -o base      Base filename for output files (default: errors.log). Writes:
#                <base>           URLs only (one per line)
#                debug_<base>     Full output per failure (URL, exit_code, script output)
#   -v, --verbose  Print debug info to stderr (disables progress bar)
#   -n, --dry-run  Print mirror commands that would run, do not execute them
#
# Environment:
#   NOSTR_SECRET_KEY  Required. Used to sign mirror requests.
#
# Requires: curl, jq. Uses scripts/mirror_hash.sh and scripts/gen_auth_header.sh (run from repo root).
#

die() {
    echo "Error: $*" >&2
    exit 1
}

verb() {
    [[ -n "${VERBOSE:-}" ]] && echo "[DEBUG] $*" >&2
}

progress_line() {
    [[ -z "${VERBOSE:-}" ]] && printf "\r%s" "$*" >&2
}

progress_bar() {
    local cur="$1" tot="$2" w="${3:-24}" prefix="${4:-}"
    [[ -n "${VERBOSE:-}" ]] && return 0
    [[ -n "${DRY_RUN:-}" ]] && return 0
    local filled=0
    [[ -n "$tot" && "$tot" -gt 0 ]] && filled=$(( (cur * w) / tot ))
    [[ $filled -gt $w ]] && filled=$w
    local empty=$((w - filled))
    local bar=""
    local k
    for ((k=0; k<filled; k++)); do bar="${bar}#"; done
    for ((k=0; k<empty; k++)); do bar="${bar} "; done
    printf "\r%s[%s] %d/%d    " "$prefix" "$bar" "$cur" "$tot" >&2
}

progress_clear() {
    [[ -z "${VERBOSE:-}" ]] && printf "\r%*s\r\n" 80 "" >&2
}

SERVER_URL=""
INPUT_FILE=""
LOG_BASE="errors.log"
VERBOSE=""
DRY_RUN=""

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        -o)
            if [[ -z "${2:-}" ]]; then
                die "-o requires a base filename"
            fi
            LOG_BASE="$2"
            shift 2
            ;;
        -v|--verbose)
            VERBOSE=1
            shift
            ;;
        -n|--dry-run)
            DRY_RUN=1
            shift
            ;;
        -*)
            die "Unknown option: $1"
            ;;
        *)
            if [[ -z "$SERVER_URL" ]]; then
                SERVER_URL="$1"
            elif [[ -z "$INPUT_FILE" ]]; then
                INPUT_FILE="$1"
            else
                die "Unexpected argument: $1"
            fi
            shift
            ;;
    esac
done

if [[ -z "${DRY_RUN:-}" ]] && [[ -z "${NOSTR_SECRET_KEY:-}" ]]; then
    die "NOSTR_SECRET_KEY is not set. Export it and retry."
fi

if [[ -z "$SERVER_URL" ]] || [[ -z "$INPUT_FILE" ]]; then
    echo "Usage: $0 <server_url> <list_missing_blobs_output_file> [-o base] [-v|--verbose] [-n|--dry-run]" >&2
    echo "  server_url   Target server to equalize" >&2
    echo "  output_file  File from list_missing_blobs -o (lines: hash source_url inferred_url)" >&2
    echo "  -o base      Log base name (default: errors.log). Writes <base> (URLs) and debug_<base> (full output)" >&2
    echo "  -v, --verbose  Debug output to stderr" >&2
    echo "  -n, --dry-run  Show commands, do not run mirror" >&2
    echo "" >&2
    echo "Requires NOSTR_SECRET_KEY in environment." >&2
    exit 1
fi

if [[ ! -f "$INPUT_FILE" ]]; then
    die "Input file not found: $INPUT_FILE"
fi

# Log file = base (URLs only). Debug log = debug_<basename> in same dir as base.
LOG_FILE="$LOG_BASE"
LOG_DIR="${LOG_BASE%/*}"
LOG_NAME="${LOG_BASE##*/}"
if [[ "$LOG_DIR" = "$LOG_BASE" ]]; then
    DEBUG_LOG_FILE="debug_$LOG_NAME"
else
    DEBUG_LOG_FILE="${LOG_DIR}/debug_${LOG_NAME}"
fi
verb "server_url=$SERVER_URL input_file=$INPUT_FILE log_file=$LOG_FILE debug_log_file=$DEBUG_LOG_FILE dry_run=${DRY_RUN:-0}"

REPO_ROOT=""
REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
if [[ -z "$REPO_ROOT" || ! -d "$REPO_ROOT" ]]; then
    die "Could not resolve repo root"
fi
verb "REPO_ROOT=$REPO_ROOT"

MIRROR_SCRIPT="$REPO_ROOT/scripts/mirror_hash.sh"
if [[ ! -f "$MIRROR_SCRIPT" ]]; then
    die "mirror_hash.sh not found: $MIRROR_SCRIPT"
fi
if [[ ! -x "$MIRROR_SCRIPT" ]]; then
    die "mirror_hash.sh not executable: $MIRROR_SCRIPT"
fi
verb "MIRROR_SCRIPT=$MIRROR_SCRIPT"

# Normalize server URL (no trailing slash for consistency with mirror_hash)
SERVER_URL="${SERVER_URL%/}"
verb "SERVER_URL (normalized)=$SERVER_URL"

# Clear log and debug log so we only have this run's failures (skip in dry-run)
if [[ -z "${DRY_RUN:-}" ]]; then
    if ! : > "$LOG_FILE"; then
        die "Could not write to log file: $LOG_FILE"
    fi
    if ! : > "$DEBUG_LOG_FILE"; then
        die "Could not write to debug log file: $DEBUG_LOG_FILE"
    fi
    verb "Cleared log file: $LOG_FILE and debug log: $DEBUG_LOG_FILE"
fi
if [[ -n "${DRY_RUN:-}" ]]; then
    verb "Dry-run: will not execute mirror, only print commands"
fi

n=0
n=$(wc -l < "$INPUT_FILE" 2>/dev/null) || true
n=$((n + 0))
verb "Total lines in input: $n"

i=0
failed=0
while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    i=$((i + 1))
    if [[ -z "${VERBOSE:-}" ]] && [[ -z "${DRY_RUN:-}" ]]; then
        progress_bar "$i" "$n" 24 "Mirror: "
    fi
    # Lines are "hash source_url inferred_url" (source_url or inferred_url may be empty)
    read -r hash source_url inferred_url <<< "$line"
    url="$source_url"
    [[ -z "$url" ]] && url="$inferred_url"
    if [[ -z "$url" ]]; then
        verb "Line $i: skip (no URL) hash=$hash"
        if [[ -z "${DRY_RUN:-}" ]]; then
            if ! echo "$line" >> "$LOG_FILE"; then
                die "Could not append to log file: $LOG_FILE"
            fi
            if ! { echo "---"; echo "URL: (none)"; echo "output: (no URL in line)"; echo "$line"; echo ""; } >> "$DEBUG_LOG_FILE"; then
                die "Could not append to debug log file: $DEBUG_LOG_FILE"
            fi
        fi
        failed=$((failed + 1))
        continue
    fi
    mirror_cmd="cd \"$REPO_ROOT\" && ./scripts/mirror_hash.sh --hash \"$hash\" \"$url\" \"$SERVER_URL\""
    verb "Line $i: mirror url=$url (hash=$hash)"
    verb "  Running: $mirror_cmd"
    if [[ -n "${DRY_RUN:-}" ]]; then
        echo "$mirror_cmd" >&2
        continue
    fi
    mirror_out=$(cd "$REPO_ROOT" && ./scripts/mirror_hash.sh --hash "$hash" "$url" "$SERVER_URL" 2>&1)
    mirror_ec=$?
    if [[ $mirror_ec -ne 0 ]]; then
        verb "  mirror failed exit_code=$mirror_ec"
        verb "  mirror output: $mirror_out"
        if [[ -n "${VERBOSE:-}" ]]; then
            echo "$mirror_out" >&2
        fi
        if ! echo "$url" >> "$LOG_FILE"; then
            die "Could not append to log file: $LOG_FILE"
        fi
        if ! { echo "---"; echo "URL: $url"; echo "exit_code: $mirror_ec"; echo "output:"; echo "$mirror_out"; echo ""; } >> "$DEBUG_LOG_FILE"; then
            die "Could not append to debug log file: $DEBUG_LOG_FILE"
        fi
        failed=$((failed + 1))
    else
        verb "  mirror ok"
    fi
done < "$INPUT_FILE"

if [[ -z "${VERBOSE:-}" ]]; then
    progress_clear
fi

verb "Done. failed=$failed log_file=$LOG_FILE debug_log_file=$DEBUG_LOG_FILE"
if [[ -n "${DRY_RUN:-}" ]]; then
    echo "Done (dry-run). Commands were printed to stderr." >&2
else
    echo "Done. Failed URLs: $LOG_FILE  Full debug: $DEBUG_LOG_FILE" >&2
fi
