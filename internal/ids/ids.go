// Package ids generates prefixed, sortable identifiers and derives the opaque
// hashes used for session tokens and idempotency fingerprints.
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

var encoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

var (
	mu       sync.Mutex
	sequence uint32
)

// Generator produces identifiers. Tests substitute a deterministic
// implementation so persisted rows are comparable.
type Generator interface {
	NewID(prefix string) string
	NewToken() (string, error)
}

// RandomGenerator is the production identifier source.
type RandomGenerator struct{}

// NewID builds a prefixed identifier with a monotonic component.
func (RandomGenerator) NewID(prefix string) string {
	mu.Lock()
	sequence++
	seq := sequence
	mu.Unlock()

	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		// rand.Read on supported platforms does not fail; fall back to the
		// monotonic counter so identifier generation never panics.
		for i := range buf {
			buf[i] = byte(seq >> (uint(i%4) * 8))
		}
	}
	stamp := uint64(time.Now().UTC().UnixNano())
	head := []byte{
		byte(stamp >> 40), byte(stamp >> 32), byte(stamp >> 24),
		byte(stamp >> 16), byte(stamp >> 8), byte(stamp),
	}
	return fmt.Sprintf("%s_%s%s", prefix, encoding.EncodeToString(head), encoding.EncodeToString(buf)[:8])
}

// NewToken builds an opaque bearer token.
func (RandomGenerator) NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("ids: read random token: %w", err)
	}
	return encoding.EncodeToString(buf), nil
}

// SequenceGenerator produces stable identifiers for tests.
type SequenceGenerator struct {
	mu      sync.Mutex
	counter int
}

// NewID returns prefix_000001 style identifiers.
func (s *SequenceGenerator) NewID(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	return fmt.Sprintf("%s_%06d", prefix, s.counter)
}

// NewToken returns a deterministic token.
func (s *SequenceGenerator) NewToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	return fmt.Sprintf("token%06d", s.counter), nil
}

// HashToken derives the stored representation of a bearer token. Raw tokens are
// never persisted.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte("cutvideo/session/" + token))
	return hex.EncodeToString(sum[:])
}

// Fingerprint hashes an arbitrary payload for idempotency comparison.
func Fingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

// Checksum derives the content checksum used by media ingest.
func Checksum(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
