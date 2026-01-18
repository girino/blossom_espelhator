package auth

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// AuthError represents an authentication error
type AuthError struct {
	Reason string
	Code   int // HTTP status code
}

func (e *AuthError) Error() string {
	return e.Reason
}

// ParseAuthorizationHeader parses the Authorization header and returns the Nostr event
// Format: "Authorization: Nostr <base64-encoded-event-json>"
func ParseAuthorizationHeader(authHeader string) (*nostr.Event, error) {
	if authHeader == "" {
		return nil, &AuthError{Reason: "Authorization header not found", Code: http.StatusUnauthorized}
	}

	// Check if it starts with "Nostr "
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || strings.ToLower(parts[0]) != "nostr" {
		return nil, &AuthError{Reason: "Authorization header must use Nostr scheme", Code: http.StatusUnauthorized}
	}

	// Decode base64
	eventJSON, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, &AuthError{Reason: "Failed to decode base64 authorization token", Code: http.StatusUnauthorized}
	}

	// Parse JSON event
	var event nostr.Event
	if err := json.Unmarshal(eventJSON, &event); err != nil {
		return nil, &AuthError{Reason: "Failed to parse authorization event", Code: http.StatusUnauthorized}
	}

	return &event, nil
}

// ValidateEvent validates a Nostr authorization event per BUD-01
// Returns error with HTTP status code if validation fails
func ValidateEvent(event *nostr.Event, requiredVerb string, allowedPubkeys map[string]bool, verbose bool) error {
	if event == nil {
		return &AuthError{Reason: "Authorization event not found", Code: http.StatusUnauthorized}
	}

	// 1. Check kind is 24242
	if event.Kind != 24242 {
		return &AuthError{Reason: fmt.Sprintf("Invalid event kind: expected 24242, got %d", event.Kind), Code: http.StatusUnauthorized}
	}

	// 2. Check pubkey format (must be 64 hex characters)
	if len(event.PubKey) != 64 {
		return &AuthError{Reason: fmt.Sprintf("Invalid pubkey format: must be 64 hex characters, got %d", len(event.PubKey)), Code: http.StatusUnauthorized}
	}

	// Validate hex format
	if _, err := hex.DecodeString(event.PubKey); err != nil {
		return &AuthError{Reason: "Invalid pubkey format: not valid hex", Code: http.StatusUnauthorized}
	}

	// 3. Verify signature using go-nostr
	valid, err := event.CheckSignature()
	if err != nil {
		if verbose {
			log.Printf("[DEBUG] Auth: signature verification error: %v", err)
		}
		return &AuthError{Reason: fmt.Sprintf("Failed to verify signature: %v", err), Code: http.StatusUnauthorized}
	}
	if !valid {
		return &AuthError{Reason: "Invalid signature", Code: http.StatusUnauthorized}
	}

	// 4. Check pubkey is in allowed list
	if len(allowedPubkeys) > 0 {
		pubkeyLower := strings.ToLower(event.PubKey)
		if !allowedPubkeys[pubkeyLower] {
			return &AuthError{Reason: "Pubkey not in allowed list", Code: http.StatusForbidden}
		}
	}

	if verbose {
		log.Printf("[DEBUG] Auth: validated event - pubkey=%s", event.PubKey)
	}

	return nil
}

// normalizePubkey converts a pubkey string (hex or npub format) to normalized hex format (lowercase, 64 chars)
// Returns the hex pubkey and an error if conversion fails
func normalizePubkey(input string) (string, error) {
	input = strings.TrimSpace(input)

	// Check if it's npub format
	if strings.HasPrefix(strings.ToLower(input), "npub") {
		// Decode npub to hex
		typ, data, err := nip19.Decode(input)
		if err != nil {
			return "", fmt.Errorf("failed to decode npub: %w", err)
		}

		// Verify it's a public key
		if typ != "npub" {
			return "", fmt.Errorf("decoded type is not npub: %s", typ)
		}

		// Extract hex pubkey from decoded data
		// nip19.Decode may return string or []byte depending on version
		var pubkeyHex string
		switch v := data.(type) {
		case string:
			pubkeyHex = v
		case []byte:
			pubkeyHex = hex.EncodeToString(v)
		default:
			return "", fmt.Errorf("unexpected data type from nip19.Decode for public key: %T", data)
		}

		// Normalize to lowercase hex
		pubkeyHex = strings.ToLower(pubkeyHex)

		// Validate hex format
		if len(pubkeyHex) != 64 {
			return "", fmt.Errorf("decoded pubkey has wrong length: %d (expected 64)", len(pubkeyHex))
		}

		if _, err := hex.DecodeString(pubkeyHex); err != nil {
			return "", fmt.Errorf("decoded pubkey is not valid hex: %w", err)
		}

		return pubkeyHex, nil
	}

	// Assume it's already hex format
	pubkeyHex := strings.ToLower(input)

	// Validate hex format
	if len(pubkeyHex) != 64 {
		return "", fmt.Errorf("hex pubkey has wrong length: %d (expected 64)", len(pubkeyHex))
	}

	if _, err := hex.DecodeString(pubkeyHex); err != nil {
		return "", fmt.Errorf("pubkey is not valid hex: %w", err)
	}

	return pubkeyHex, nil
}

// BuildAllowedPubkeysMap builds a map from a slice of pubkey strings (hex or npub format) for fast lookup
// All pubkeys are normalized to lowercase hex format
func BuildAllowedPubkeysMap(allowedPubkeys []string) map[string]bool {
	m := make(map[string]bool)
	for _, pubkey := range allowedPubkeys {
		normalized, err := normalizePubkey(pubkey)
		if err != nil {
			log.Printf("[WARN] Invalid pubkey in allowed_pubkeys configuration: %s (error: %v)", pubkey, err)
			continue
		}
		m[normalized] = true
	}
	return m
}

// ValidateAuth validates the Authorization header for a request
// Returns the pubkey if valid, or an error with HTTP status code
func ValidateAuth(r *http.Request, requiredVerb string, allowedPubkeys map[string]bool, verbose bool) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", &AuthError{Reason: "Authorization header not found", Code: http.StatusUnauthorized}
	}

	event, err := ParseAuthorizationHeader(authHeader)
	if err != nil {
		return "", err
	}

	if err := ValidateEvent(event, requiredVerb, allowedPubkeys, verbose); err != nil {
		return "", err
	}

	return strings.ToLower(event.PubKey), nil
}
