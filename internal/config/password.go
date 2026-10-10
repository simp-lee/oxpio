package config

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

const (
	argon2Version    = 19
	argon2Memory     = 19 * 1024
	argon2Iterations = 2
	argon2Parallel   = 1
	argon2SaltLength = 16
	argon2KeyLength  = 32
)

// ValidateArgon2idPasswordHash accepts the standard PHC Argon2id encoding
// used by the edit account configuration.
func ValidateArgon2idPasswordHash(value string) error {
	_, err := parseArgon2idHash(strings.TrimSpace(value))
	return err
}

// HashArgon2idPassword creates a PHC Argon2id hash for interactive setup.
func HashArgon2idPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password must be non-empty")
	}
	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2Memory, argon2Parallel, argon2KeyLength)
	return formatArgon2idHash(salt, key), nil
}

// VerifyArgon2idPassword compares a plaintext password to a configured PHC
// Argon2id hash without exposing the derived key through ordinary equality.
func VerifyArgon2idPassword(password, encoded string) (bool, error) {
	hash, err := parseArgon2idHash(strings.TrimSpace(encoded))
	if err != nil {
		return false, err
	}
	key := argon2.IDKey([]byte(password), hash.salt, hash.iterations, hash.memory, hash.parallel, uint32(len(hash.key)))
	return subtle.ConstantTimeCompare(key, hash.key) == 1, nil
}

type parsedArgon2idHash struct {
	memory     uint32
	iterations uint32
	parallel   uint8
	salt       []byte
	key        []byte
}

func parseArgon2idHash(value string) (parsedArgon2idHash, error) {
	var result parsedArgon2idHash
	parts := strings.Split(value, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return result, fmt.Errorf("passwordHash must be a standard Argon2id PHC hash")
	}
	params := make(map[string]uint64, 3)
	for _, item := range strings.Split(parts[3], ",") {
		name, raw, ok := strings.Cut(item, "=")
		if !ok || (name != "m" && name != "t" && name != "p") || raw == "" {
			return result, fmt.Errorf("passwordHash must be a standard Argon2id PHC hash")
		}
		if _, exists := params[name]; exists {
			return result, fmt.Errorf("passwordHash must be a standard Argon2id PHC hash")
		}
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || parsed == 0 {
			return result, fmt.Errorf("passwordHash must be a standard Argon2id PHC hash")
		}
		params[name] = parsed
	}
	if len(params) != 3 || params["m"] < 8*params["p"] || params["m"] > 1<<20 || params["t"] > 32 || params["p"] == 0 || params["p"] > 32 {
		return result, fmt.Errorf("passwordHash must be a standard Argon2id PHC hash")
	}
	decode := func(raw string) ([]byte, error) {
		if strings.ContainsAny(raw, "= \t\r\n") {
			return nil, fmt.Errorf("invalid base64")
		}
		return base64.RawStdEncoding.DecodeString(raw)
	}
	var err error
	result.salt, err = decode(parts[4])
	if err != nil || len(result.salt) < 8 || len(result.salt) > 1024 {
		return parsedArgon2idHash{}, fmt.Errorf("passwordHash must be a standard Argon2id PHC hash")
	}
	result.key, err = decode(parts[5])
	if err != nil || len(result.key) < 16 || len(result.key) > 1024 {
		return parsedArgon2idHash{}, fmt.Errorf("passwordHash must be a standard Argon2id PHC hash")
	}
	result.memory = uint32(params["m"])
	result.iterations = uint32(params["t"])
	result.parallel = uint8(params["p"])
	return result, nil
}

func formatArgon2idHash(salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2Version, argon2Memory, argon2Iterations, argon2Parallel,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func validateEditorConfig(username, passwordHash string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("editor.username must be non-empty")
	}
	for _, r := range username {
		if unicode.IsControl(r) || r == '\n' || r == '\r' {
			return fmt.Errorf("editor.username must not contain control characters or newlines")
		}
	}
	if strings.TrimSpace(passwordHash) == "" {
		return fmt.Errorf("editor.passwordHash must be non-empty")
	}
	if err := ValidateArgon2idPasswordHash(passwordHash); err != nil {
		return fmt.Errorf("editor.passwordHash: %w", err)
	}
	return nil
}
