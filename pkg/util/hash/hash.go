package hash

import (
	"crypto/sha256"
	"fmt"
	"io"
)

// String hashes a given string
func String(s string) string {
	hash := sha256.New()
	io.WriteString(hash, s)

	return fmt.Sprintf("%x", hash.Sum(nil))
}
