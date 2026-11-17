// Package vmevidence contains pure validation primitives for disposable-VM
// evidence. It has no process, filesystem, network, or VM control capability.
package vmevidence

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	MaxManifestBytes = 1 << 20
	MaxJSONDepth     = 16
	MaxObjectMembers = 64
	MaxArrayEntries  = 512
	MaxStringBytes   = 4096
)

var (
	ErrInvalidEvidence = errors.New("invalid VM evidence")

	// Checkpoint outcome classes are backend-neutral; adapters wrap these
	// sentinels so callers never need to parse backend error strings.
	ErrCheckpointNotFound       = errors.New("checkpoint not found")
	ErrCheckpointConflict       = errors.New("checkpoint compare-and-advance conflict")
	ErrCheckpointOutcomeUnknown = errors.New("checkpoint commit outcome unknown")
	ErrCheckpointUnavailable    = errors.New("checkpoint authority unavailable")
	ErrCheckpointCorrupt        = errors.New("checkpoint authority state corrupt or rollback suspected")
)

// SignatureEnvelope is detached from the exact manifest bytes.
type SignatureEnvelope struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Digest    string `json:"digest"`
	Signature string `json:"signature"`
}

// ValidateStrictJSON checks UTF-8, bounded structure, duplicate keys and
// trailing data. It intentionally does not interpret the manifest schema.
func ValidateStrictJSON(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxManifestBytes || !utf8.Valid(raw) {
		return fmt.Errorf("%w: size or UTF-8 invalid", ErrInvalidEvidence)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := consumeBounded(dec, 0); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvidence, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrInvalidEvidence)
		}
		return fmt.Errorf("%w: trailing data: %v", ErrInvalidEvidence, err)
	}
	return nil
}

func consumeBounded(dec *json.Decoder, depth int) error {
	if depth > MaxJSONDepth {
		return errors.New("JSON nesting limit exceeded")
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if s, ok := tok.(string); ok && len(s) > MaxStringBytes {
		return errors.New("JSON string limit exceeded")
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := make(map[string]struct{}, 8)
		count := 0
		for dec.More() {
			count++
			if count > MaxObjectMembers {
				return errors.New("JSON object member limit exceeded")
			}
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := kt.(string)
			if !ok {
				return errors.New("non-string object key")
			}
			if len(key) > MaxStringBytes {
				return errors.New("JSON key limit exceeded")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeBounded(dec, depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("malformed object")
		}
	case '[':
		count := 0
		for dec.More() {
			count++
			if count > MaxArrayEntries {
				return errors.New("JSON array entry limit exceeded")
			}
			if err := consumeBounded(dec, depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("malformed array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

// VerifyDetachedSignature verifies the exact bytes supplied by the caller.
// The key must come from an out-of-band trust store; this function does not
// establish host provenance or validate manifest semantics or anti-replay.
func VerifyDetachedSignature(raw []byte, envelope SignatureEnvelope, trustedKeys map[string]ed25519.PublicKey) error {
	if err := ValidateStrictJSON(raw); err != nil {
		return err
	}
	if envelope.Algorithm != "Ed25519-SHA256" || envelope.KeyID == "" {
		return fmt.Errorf("%w: unsupported signature envelope", ErrInvalidEvidence)
	}
	key, ok := trustedKeys[envelope.KeyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: untrusted signing key", ErrInvalidEvidence)
	}
	digest := sha256.Sum256(raw)
	if envelope.Digest != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("%w: digest mismatch", ErrInvalidEvidence)
	}
	sig, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, digest[:], sig) {
		return fmt.Errorf("%w: signature verification failed", ErrInvalidEvidence)
	}
	return nil
}
