package gofer

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type SecureGofer struct {
	rootPath  string
	secretKey []byte
}

func NewSecureGofer(root string) *SecureGofer {
	return &SecureGofer{
		rootPath:  root,
		secretKey: []byte("apex-singularity-omni-hmac-secret-v8-2026"),
	}
}

func (g *SecureGofer) IssueCapabilityToken(cleanPath string, permissions string) (string, error) {
	timestamp := time.Now().Add(4 * time.Hour).Unix()
	payload := fmt.Sprintf("%s:%s:%d", cleanPath, permissions, timestamp)

	mac := hmac.New(sha256.New, g.secretKey)
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("%s.%d.%s", hex.EncodeToString([]byte(payload)), timestamp, signature), nil
}

func (g *SecureGofer) CanonicalPathSeal(userInput string) (string, error) {
	cleaned := filepath.Clean("/" + userInput)
	if strings.Contains(cleaned, "..") || strings.Contains(userInput, " ") {
		return "", errors.New("security violation: path traversal or null-byte payload detected")
	}
	return filepath.Join(g.rootPath, cleaned), nil
}
