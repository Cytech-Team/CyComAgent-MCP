package vmevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

const (
	MaxEvidenceArtifactCount = 2048
	MaxEvidenceArtifactBytes = 64 << 20
	MaxSingleArtifactBytes   = 16 << 20
)

// ArtifactVerification reports byte-level checks for artifacts referenced by a
// decoded manifest. Artifact bytes must be supplied by a separately authorized
// reader; this function never opens paths or trusts manifest-provided locations.
type ArtifactVerification struct {
	ArtifactCount int
	TotalBytes    int64
	Verified      []string
}

// VerifyManifestArtifacts recomputes every fixture, case-evidence and teardown
// digest from caller-supplied bytes. The map key must be the lowercase SHA-256
// digest referenced by the manifest. Extra, missing, oversized or mismatched
// artifacts fail closed. This verifies byte integrity only, not provenance.
func VerifyManifestArtifacts(m Manifest, artifacts map[string][]byte) (ArtifactVerification, error) {
	var report ArtifactVerification
	if err := validateManifestStructure(m); err != nil {
		return report, err
	}
	if len(artifacts) == 0 {
		return report, fmt.Errorf("%w: no evidence artifacts supplied", ErrInvalidEvidence)
	}
	if len(artifacts) > MaxEvidenceArtifactCount {
		return report, fmt.Errorf("%w: evidence artifact count limit exceeded", ErrInvalidEvidence)
	}

	referenced := make(map[string]struct{}, len(artifacts))
	add := func(digest string, expectedSize *uint64) error {
		if !sha256Pattern.MatchString(digest) {
			return fmt.Errorf("%w: invalid referenced artifact digest", ErrInvalidEvidence)
		}
		data, ok := artifacts[digest]
		if !ok {
			return fmt.Errorf("%w: missing artifact bytes for digest %s", ErrInvalidEvidence, digest)
		}
		if len(data) > MaxSingleArtifactBytes {
			return fmt.Errorf("%w: individual evidence artifact exceeds size limit", ErrInvalidEvidence)
		}
		if expectedSize != nil && uint64(len(data)) != *expectedSize {
			return fmt.Errorf("%w: fixture artifact size mismatch for digest %s", ErrInvalidEvidence, digest)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != digest {
			return fmt.Errorf("%w: artifact digest mismatch for %s", ErrInvalidEvidence, digest)
		}
		if _, exists := referenced[digest]; !exists {
			referenced[digest] = struct{}{}
			report.TotalBytes += int64(len(data))
			if report.TotalBytes > MaxEvidenceArtifactBytes {
				return fmt.Errorf("%w: total evidence artifact size limit exceeded", ErrInvalidEvidence)
			}
		}
		return nil
	}

	for _, fixture := range m.Fixtures {
		size := fixture.SizeBytes
		if err := add(fixture.SHA256, &size); err != nil {
			return ArtifactVerification{}, fmt.Errorf("fixture %s: %w", fixture.FixtureID, err)
		}
	}
	for _, c := range m.Cases {
		for _, evidence := range c.Evidence {
			if err := add(evidence.Digest, nil); err != nil {
				return ArtifactVerification{}, fmt.Errorf("case %s evidence: %w", c.CaseID, err)
			}
		}
	}
	if err := add(m.Teardown.EvidenceDigest, nil); err != nil {
		return ArtifactVerification{}, fmt.Errorf("teardown evidence: %w", err)
	}
	if len(referenced) != len(artifacts) {
		return ArtifactVerification{}, fmt.Errorf("%w: unreferenced evidence artifacts supplied", ErrInvalidEvidence)
	}
	report.ArtifactCount = len(referenced)
	report.Verified = make([]string, 0, len(referenced))
	for digest := range referenced {
		report.Verified = append(report.Verified, digest)
	}
	// Sort to keep reports deterministic without exposing artifact contents.
	sortStrings(report.Verified)
	return report, nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
