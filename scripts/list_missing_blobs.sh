#!/usr/bin/env bash
#
# List blobs that are missing on each upstream server for a given pubkey.
# Reads upstream_servers from config, lists blobs on each, diffs against the union,
# then verifies each "missing" blob with HEAD (removes from list if HEAD 200).
# Servers with supports_mirror: false are source-only: we call list (for union/source) but skip missing/HEAD and do not output a section for them.
#
# Usage: ./scripts/list_missing_blobs.sh <pubkey> [config_path] [-json] [-v] [-o dir]
#
# Requires: jq, curl, yq (https://github.com/mikefarah/yq). For auth: nak + gen_auth_header.sh.
# -o dir: write one file per server in dir (filename = server URL without scheme/slashes); each line is "hash source_url inferred_url" (source_url = exact .url from list; inferred_url = server_base/hash.ext, ext from source_url).
# Environment: NOSTR_SECRET_KEY (for auth). Optional: BLOSSOM_PUBKEY.
#

die() {
    echo "Error: $*" >&2
    exit 1
}

verb() {
    [[ -n "${VERBOSE:-}" ]] && echo "[DEBUG] $*" >&2
}

# Progress bar / line (only when not verbose, output to stderr).
# Alternative: dialog --gauge or whiptail --gauge feed percent (0-100) per line, no pipe-byte counting.
progress_line() {
    [[ -z "${VERBOSE:-}" ]] && printf "\r%s" "$*" >&2
}

progress_bar() {
    local cur="$1" tot="$2" w="${3:-24}" prefix="${4:-}"
    [[ -n "${VERBOSE:-}" ]] && return 0
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

# Safe filename from server URL: strip https?:// and slashes (host or host_port)
safe_output_name() {
    local url="$1"
    local x="${url#*://}"
    x="${x%%/*}"
    x="${x//:/_}"
    echo "$x"
}

PUBKEY="${1:-${BLOSSOM_PUBKEY:-}}"
CONFIG_PATH="config/config.yaml"
JSON_OUT=""
VERBOSE=""
OUT_DIR=""
[[ $# -gt 0 ]] && shift
while [[ $# -gt 0 ]]; do
    if [[ "$1" = "-json" ]]; then
        JSON_OUT=1
    elif [[ "$1" = "-v" || "$1" = "--verbose" ]]; then
        VERBOSE=1
    elif [[ "$1" = "-o" || "$1" = "--output-dir" ]]; then
        [[ -z "${2:-}" ]] && die "-o requires an output directory path"
        OUT_DIR="$2"
        shift
    elif [[ "$1" = -o=* ]]; then
        OUT_DIR="${1#-o=}"
    elif [[ "$1" = --output-dir=* ]]; then
        OUT_DIR="${1#--output-dir=}"
    else
        CONFIG_PATH="$1"
    fi
    shift
done

if [[ -z "$PUBKEY" ]]; then
    echo "Usage: $0 <pubkey> [config_path] [-json] [-v|--verbose] [-o dir]" >&2
    echo "  pubkey: hex or npub (required, or set BLOSSOM_PUBKEY)" >&2
    echo "  config_path: default config/config.yaml" >&2
    echo "  -v, --verbose: print debug info to stderr" >&2
    echo "  -o, --output-dir dir: write one file per server in dir (filename = server without https?:// and slashes)" >&2
    exit 1
fi

for req in jq curl yq; do
    if ! command -v "$req" &>/dev/null; then
        die "$req required (apt install jq curl; yq: https://github.com/mikefarah/yq)"
    fi
done

# Resolve config_path when second arg is not -json
if [[ "${2:-}" != "-json" && -n "${2:-}" ]]; then
    CONFIG_PATH="$2"
fi

if [[ ! -f "$CONFIG_PATH" ]]; then
    die "config not found: $CONFIG_PATH"
fi

if [[ -n "$OUT_DIR" ]]; then
    if ! mkdir -p "$OUT_DIR"; then
        die "could not create output directory: $OUT_DIR"
    fi
fi

# Normalize pubkey to hex (same logic as list_pubkey.sh)
HEX_PUBKEY=""
if echo "$PUBKEY" | grep -qE '^npub1'; then
    if ! command -v nak &>/dev/null; then
        die "npub given but 'nak' not found (hex pubkey or install nak)"
    fi
    HEX_PUBKEY=$(nak decode "$PUBKEY" 2>/dev/null | tr -d '[:space:]')
    if [[ -z "$HEX_PUBKEY" || ${#HEX_PUBKEY} -ne 64 ]]; then
        die "failed to decode npub"
    fi
else
    HEX_PUBKEY=$(echo "$PUBKEY" | tr -d '[:space:]' | tr '[:upper:]' '[:lower:]')
    if ! echo "$HEX_PUBKEY" | grep -qE '^[0-9a-f]{64}$'; then
        die "invalid pubkey (need 64 hex or npub1...)"
    fi
fi

verb "config $CONFIG_PATH pubkey ${HEX_PUBKEY:0:8}..."

# Parse upstream servers: output "base_url connect_url priority supports_mirror" per line
# supports_mirror: true = destination (we compute missing + HEAD check); false = source-only (list only, no missing/HEAD)
yq_out=$(yq -r '
  .upstream_servers[] | [.url, (if .alternative_address then .alternative_address else .url end), (.priority // 999), (if .supports_mirror == false then "0" else "1" end)] | @tsv
' "$CONFIG_PATH" 2>/dev/null) || die "failed to parse config with yq"
LINES=()
while IFS= read -r line; do
    [[ -n "$line" ]] && LINES+=("$line")
done <<< "$yq_out"

# Temp dir for per-server lists and union
TMP=$(mktemp -d)
if [[ -z "$TMP" || ! -d "$TMP" ]]; then
    die "mktemp -d failed"
fi
trap 'rm -rf "$TMP"' EXIT

# Auth header for list (same URL pattern for GET /list/<hex>)
# We'll generate per-server when we call list/HEAD
get_auth_header() {
    local method="$1"
    local url="$2"
    local dir
    dir=$(cd "$(dirname "$0")/.." && pwd)
    if [[ -n "${NOSTR_SECRET_KEY:-}" ]]; then
        "$dir/scripts/gen_auth_header.sh" "$method" "$url" 2>/dev/null || true
    fi
}

# Fetch list for one server; output "hash<TAB>url" per line (url = exact .url from list JSON, or empty)
list_server_hashes() {
    local base_url="$1"
    local connect_url="$2"
    local list_url="${connect_url%/}/list/$HEX_PUBKEY"
    verb "list $base_url GET $list_url"
    local header
    header=$(get_auth_header GET "$list_url")
    local resp body code
    resp=$(curl -sS -w "\n%{http_code}" -X GET ${header:+-H "$header"} "$list_url" 2>/dev/null) || true
    body=$(echo "$resp" | head -n -1)
    code=$(echo "$resp" | tail -n 1)
    if [[ "$code" != "200" ]]; then
        verb "list $base_url failed (HTTP $code)"
        echo "[WARN] list failed for $base_url (HTTP $code)" >&2
        return
    fi
    echo "$body" | jq -r '.[] | ((.sha256 // .hash) // "") as $h | select($h | test("^[0-9a-fA-F]{64}$")) | "\($h | ascii_downcase)\t\(.url // "")"'
}

# HEAD timeout in seconds (curl -m)
HEAD_TIMEOUT="${HEAD_TIMEOUT:-2}"

# HEAD to a server for path; 0 if 200, non-zero otherwise
head_ok() {
    local connect_url="$1"
    local hash="$2"
    local url="${connect_url%/}/$hash"
    verb "HEAD $url (request)"
    local header
    header=$(get_auth_header HEAD "$url")
    verb "HEAD command: curl -sS -L -m $HEAD_TIMEOUT -o /dev/null -w '%{http_code}' -I${header:+ -H \"$header\"} \"$url\""
    local code
    code=$(curl -sS -L -m "$HEAD_TIMEOUT" -o /dev/null -w "%{http_code}" -I ${header:+-H "$header"} "$url" 2>/dev/null) || echo "000"
    verb "HEAD $url -> $code (response)"
    [[ "$code" = "200" ]]
}

# Build list of "base_url connect_url", priority, and supports_mirror per index
SERVERS=()
PRIORITIES=()
SUPPORTS_MIRROR=()
for line in "${LINES[@]}"; do
    [[ -z "$line" ]] && continue
    base="${line%%$'\t'*}"
    rest="${line#*$'\t'}"
    connect="${rest%%$'\t'*}"
    rest2="${rest#*$'\t'}"
    prio="${rest2%%$'\t'*}"
    supports_mirror="${rest2#*$'\t'}"
    base="${base//\"/}"
    connect="${connect//\"/}"
    SERVERS+=("$base $connect")
    PRIORITIES+=("${prio:-999}")
    SUPPORTS_MIRROR+=("${supports_mirror:-1}")
done
verb "parsed ${#SERVERS[@]} upstream servers"

# First server by priority (lowest number = best) that has it, excluding exclude_i. Outputs "url<TAB>inferred" (tab).
# url = exact .url from that server's list item; inferred = base/hash.ext (ext from url filename, blossom standard).
get_source_for_hash() {
    local hash="$1" exclude_i="$2"
    local j url base connect filename ext inferred
    while IFS= read -r line; do
        j="${line##* }"
        [[ "$j" -eq "$exclude_i" ]] && continue
        grep -qxF "$hash" "$TMP/server_$j.txt" 2>/dev/null || continue
        url=$(awk -F'\t' -v h="$hash" '$1==h{print $2; exit}' "$TMP/server_${j}_map.txt" 2>/dev/null)
        read -r base connect <<< "${SERVERS[$j]}"
        inferred=""
        if [[ -n "$url" ]]; then
            filename="${url##*/}"
            if [[ "$filename" == *.* ]]; then
                ext="${filename##*.}"
                inferred="${base%/}/$hash.$ext"
            else
                inferred="${base%/}/$hash"
            fi
        fi
        printf '%s\t%s\n' "$url" "$inferred"
        return
    done < <(for j in "${!SERVERS[@]}"; do echo "${PRIORITIES[$j]:-999} $j"; done | sort -n -k1,1)
    printf '\t\n'
}

# 1) List per server -> hash+url in server_<i>_map.txt (hash<TAB>url), hashes in server_<i>.txt, union in union.txt
> "$TMP/union.txt"
nservers=${#SERVERS[@]}
for i in "${!SERVERS[@]}"; do
    [[ -z "${VERBOSE:-}" ]] && progress_line "Listing server $((i+1))/$nservers...    "
    read -r base connect <<< "${SERVERS[$i]}"
    list_server_hashes "$base" "$connect" > "$TMP/server_${i}_map.txt" || true
    cut -f1 "$TMP/server_${i}_map.txt" 2>/dev/null > "$TMP/server_$i.txt" || : > "$TMP/server_$i.txt"
    n=$(wc -l < "$TMP/server_$i.txt")
    verb "$base list: $n hashes"
    cat "$TMP/server_$i.txt" >> "$TMP/union.txt"
done
[[ -z "${VERBOSE:-}" ]] && progress_clear
sort -u "$TMP/union.txt" > "$TMP/union_sorted.txt"
verb "union: $(wc -l < "$TMP/union_sorted.txt") unique hashes"

# 2) For each server with supports_mirror: missing = union - server_hashes; then filter by HEAD
#    Source-only servers (supports_mirror=false): skip missing/HEAD, they can still be a source
declare -A MISSING_LISTS
nservers=${#SERVERS[@]}
for i in "${!SERVERS[@]}"; do
    read -r base connect <<< "${SERVERS[$i]}"
    if [[ "${SUPPORTS_MIRROR[$i]:-1}" = "0" ]]; then
        verb "skip missing check for $base (source-only, list used for union/source only)"
        continue
    fi
    # missing_candidates = union - server_i
    comm -23 "$TMP/union_sorted.txt" <(sort -u "$TMP/server_$i.txt") > "$TMP/missing_cand_$i.txt" || true
    ncand=$(wc -l < "$TMP/missing_cand_$i.txt")
    verb "$base missing_candidates: $ncand"
    # For each candidate, HEAD; if not 200, keep
    > "$TMP/missing_final_$i.txt"
    if [[ -z "${VERBOSE:-}" ]]; then
        echo "$base" >&2
    fi
    if [[ -n "${VERBOSE:-}" ]]; then
        while read -r hash; do
            [[ -z "$hash" ]] && continue
            if head_ok "$connect" "$hash"; then
                verb "$base HEAD $hash 200 (drop)"
            else
                verb "$base HEAD $hash not_200 (keep)"
                echo "$hash" >> "$TMP/missing_final_$i.txt"
            fi
        done < "$TMP/missing_cand_$i.txt"
    else
        j=0
        while read -r hash; do
            [[ -z "$hash" ]] && continue
            progress_bar $((j+1)) "$ncand" 24 "Server $((i+1))/$nservers: "
            if head_ok "$connect" "$hash"; then
                :
            else
                echo "$hash" >> "$TMP/missing_final_$i.txt"
            fi
            j=$((j+1))
        done < "$TMP/missing_cand_$i.txt"
        progress_clear
    fi
    nfinal=$(wc -l < "$TMP/missing_final_$i.txt")
    verb "$base missing_after_head: $nfinal"
    MISSING_LISTS[$i]="$TMP/missing_final_$i.txt"
done

# 3) Output: with -o only write to files; otherwise print to stdout (json or human-readable)
#    Only for destination servers (supports_mirror=true). Columns: hash, source_url, inferred_url.
if [[ -n "$OUT_DIR" ]]; then
    for i in "${!SERVERS[@]}"; do
        [[ -z "${MISSING_LISTS[$i]:-}" ]] && continue
        read -r base connect <<< "${SERVERS[$i]}"
        fname=$(safe_output_name "$base")
        : > "$OUT_DIR/$fname"
        while read -r h; do
            [[ -z "$h" ]] && continue
            result=$(get_source_for_hash "$h" "$i")
            IFS=$'\t' read -r src inferred <<< "$result"
            if [[ -n "$src" || -n "$inferred" ]]; then printf '%s %s %s\n' "$h" "$src" "$inferred" >> "$OUT_DIR/$fname"; else printf '%s\n' "$h" >> "$OUT_DIR/$fname"; fi
        done < "${MISSING_LISTS[$i]}"
    done
elif [[ -n "$JSON_OUT" ]]; then
    echo -n '{'
    first=1
    for i in "${!SERVERS[@]}"; do
        [[ -z "${MISSING_LISTS[$i]:-}" ]] && continue
        [[ $first -eq 0 ]] && echo -n ','
        first=0
        read -r base connect <<< "${SERVERS[$i]}"
        json_elems=""
        while read -r h; do
            [[ -z "$h" ]] && continue
            result=$(get_source_for_hash "$h" "$i")
            IFS=$'\t' read -r src inferred <<< "$result"
            [[ -n "$json_elems" ]] && json_elems+=','
            json_elems+=$(jq -n -c --arg h "$h" --arg s "$src" --arg i "$inferred" '{hash:$h,source:$s,inferred:$i}')
        done < "${MISSING_LISTS[$i]}"
        hashes="[${json_elems}]"
        printf '"%s": %s' "$base" "$hashes"
    done
    echo '}'
else
    for i in "${!SERVERS[@]}"; do
        [[ -z "${MISSING_LISTS[$i]:-}" ]] && continue
        read -r base connect <<< "${SERVERS[$i]}"
        echo "## $base"
        if [[ ! -s "${MISSING_LISTS[$i]}" ]]; then
            echo "  (none)"
        else
            while read -r h; do
                [[ -z "$h" ]] && continue
                result=$(get_source_for_hash "$h" "$i")
                IFS=$'\t' read -r src inferred <<< "$result"
                if [[ -n "$src" || -n "$inferred" ]]; then echo "  $h $src $inferred"; else echo "  $h"; fi
            done < "${MISSING_LISTS[$i]}"
        fi
        echo
    done
fi
