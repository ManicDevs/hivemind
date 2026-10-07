package migration

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"gitlab.torproject.org/cerberus-droid/hivemind/internal/persistence"
	"io"
	"os"
)

type MigrationPayload struct {
	SourceOS     string            `json:"source_os"`
	Instruction  uintptr           `json:"instruction_pointer"`
	StackPointer uintptr           `json:"stack_pointer"`
	Rules        map[string]string `json:"epigenetic_rules"`
}

type LiveMigrationEngine struct {
	encryptionKey []byte
}

func NewLiveMigrationEngine() *LiveMigrationEngine {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &LiveMigrationEngine{encryptionKey: key}
}

func (m *LiveMigrationEngine) SerializeAndEncryptState(targetOS string, ip uintptr, sp uintptr, mem *persistence.KernelMemory) (string, error) {
	payload := MigrationPayload{
		SourceOS:     targetOS,
		Instruction:  ip,
		StackPointer: sp,
		Rules:        mem.GetJITRules(),
	}

	plain, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(m.encryptionKey)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nonce, nonce, plain, nil)
	pkgPath := "apex_live_migration_v8.pkg"
	if err := os.WriteFile(pkgPath, ciphertext, 0600); err != nil {
		return "", err
	}

	return pkgPath, nil
}
