package storage

import (
	"fmt"
	"maps"
	"slices"
	"unicode/utf8"
)

// Service metadata limits. Every record is gossiped, and ordinary gossip
// packets stay under transport.MaxPayload (1200 bytes), so metadata is kept
// small enough that a record with it still fits alongside other updates.
const (
	MaxMetadataEntries    = 16
	MaxMetadataKeyLength  = 63
	MaxMetadataValueBytes = 256
	MaxMetadataBytes      = 512
	MaxMetadataFilters    = 8
)

// ValidateMetadata checks a service's metadata map. Keys are 1-63 characters
// of letters, digits, '.', '_', '-' and '/', starting with a letter or digit,
// so they are safe in SQLite JSON paths and query strings. Values are valid
// UTF-8 of at most 256 bytes and may be empty.
func ValidateMetadata(metadata map[string]string) error {
	if len(metadata) > MaxMetadataEntries {
		return fmt.Errorf("at most %d metadata entries are allowed", MaxMetadataEntries)
	}
	total := 0
	for key, value := range metadata {
		if !ValidMetadataKey(key) {
			return fmt.Errorf("metadata key %q must be 1-%d letters, digits, '.', '_', '-' or '/', starting with a letter or digit", key, MaxMetadataKeyLength)
		}
		if len(value) > MaxMetadataValueBytes || !utf8.ValidString(value) {
			return fmt.Errorf("metadata value for %q must be valid UTF-8 of at most %d bytes", key, MaxMetadataValueBytes)
		}
		total += len(key) + len(value)
	}
	if total > MaxMetadataBytes {
		return fmt.Errorf("metadata keys and values must total at most %d bytes", MaxMetadataBytes)
	}
	return nil
}

// ValidMetadataKey reports whether key is an allowed metadata key.
func ValidMetadataKey(key string) bool {
	if key == "" || len(key) > MaxMetadataKeyLength {
		return false
	}
	for i, r := range key {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if !alnum && (i == 0 || r != '.' && r != '_' && r != '-' && r != '/') {
			return false
		}
	}
	return true
}

// SortedMetadataKeys returns metadata keys in a stable order for encoding.
func SortedMetadataKeys(metadata map[string]string) []string {
	return slices.Sorted(maps.Keys(metadata))
}
