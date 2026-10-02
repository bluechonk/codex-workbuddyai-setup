package gateway

import (
	"crypto/rand"
	"encoding/hex"
)

// newID mints an identifier shaped like the OpenAI Responses API uses.
func newID(prefix string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return prefix + "_0000000000000000"
	}
	return prefix + "_" + hex.EncodeToString(buf)
}
