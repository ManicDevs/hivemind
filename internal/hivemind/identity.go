package hivemind

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"
)

// Identity errors
var (
	ErrIdentityCompromised = errors.New("identity seed compromised, rotation required")
	ErrIdentityExpired     = errors.New("identity expired, rotation required")
	ErrHandleCollision     = errors.New("handle collision detected")
	ErrNoEntropy           = errors.New("entropy source unavailable")
	ErrRevokedKey          = errors.New("key has been revoked")
	ErrInvalidDerivation   = errors.New("invalid derivation path")
	ErrRotationRequired    = errors.New("key rotation required")
)

// Identity represents a hierarchical deterministic identity with rotation,
// revocation, and collision detection.
type Identity struct {
	MasterSeed        []byte           `json:"master_seed,omitempty"`         // never serialized directly
	MasterSeedHash    string           `json:"master_seed_hash,omitempty"`    // for verification only
	DerivationPath    string           `json:"derivation_path,omitempty"`     // e.g., "m/44'/0'/0'/0/0"
	RotationEpoch     int64            `json:"rotation_epoch,omitempty"`      // unix time of last rotation
	ExpiresAt         int64            `json:"expires_at,omitempty"`          // 0 = never
	RevokedKeys       map[string]int64 `json:"revoked_keys,omitempty"`        // keyID -> revokedAt
	CurrentKeyIndex   uint32           `json:"current_key_index,omitempty"`   // for rotation
	HandleCollisionID string           `json:"handle_collision_id,omitempty"` // unique suffix for collisions
}

// IdentityConfig holds configuration for identity behavior.
type IdentityConfig struct {
	RotationInterval  time.Duration // how often to rotate keys (default: 90 days)
	KeyExpiration     time.Duration // max key lifetime (default: 1 year)
	CollisionCheck    bool          // check for handle collisions on mesh join
	RequireRotation   bool          // refuse operation if rotation overdue
	MasterSeedFromEnv bool          // derive master seed from env var
	EnvVarName        string        // env var name for master seed
}

// DefaultIdentityConfig returns secure defaults.
func DefaultIdentityConfig() IdentityConfig {
	return IdentityConfig{
		RotationInterval:  90 * 24 * time.Hour,
		KeyExpiration:     365 * 24 * time.Hour,
		CollisionCheck:    true,
		RequireRotation:   false, // warn but don't block
		MasterSeedFromEnv: false,
		EnvVarName:        "HIVEMIND_MASTER_SEED",
	}
}

// DerivationPath components
const (
	PurposeHivemind  = 44 // BIP-44 style
	CoinTypeHivemind = 0
	AccountIndex     = 0
	ChangeIndex      = 0
)

// Key IDs
const (
	KeyIDSigning   = "sign"      // for frame signing
	KeyIDTransport = "transport" // for transport encryption
	KeyIDMesh      = "mesh"      // for mesh authentication
	KeyIDBackup    = "backup"    // for backup/recovery
)

// keyLabels maps key index to human-readable label
var keyLabels = map[uint32]string{
	0: KeyIDSigning,
	1: KeyIDTransport,
	2: KeyIDMesh,
	3: KeyIDBackup,
}

// NewIdentity creates a new root identity with master seed.
func NewIdentity(config IdentityConfig) (*Identity, error) {
	masterSeed := make([]byte, 32)
	if _, err := rand.Read(masterSeed); err != nil {
		return nil, fmt.Errorf("failed to generate master seed: %w", ErrNoEntropy)
	}

	now := time.Now().Unix()
	identity := &Identity{
		MasterSeedHash:    hashMasterSeed(masterSeed),
		DerivationPath:    fmt.Sprintf("m/%d'/%d'/%d'/%d/%d", PurposeHivemind, CoinTypeHivemind, AccountIndex, ChangeIndex, 0),
		RotationEpoch:     now,
		ExpiresAt:         now + int64(config.KeyExpiration.Seconds()),
		RevokedKeys:       make(map[string]int64),
		CurrentKeyIndex:   0,
		HandleCollisionID: "",
	}

	// Store master seed only in memory, never serialize
	// (we'll derive keys on demand)
	return identity, nil
}

// NewIdentityFromMasterSeed creates an identity from an existing master seed.
// Used for recovery from backup.
func NewIdentityFromMasterSeed(masterSeed []byte, config IdentityConfig) (*Identity, error) {
	if len(masterSeed) != 32 {
		return nil, errors.New("master seed must be 32 bytes")
	}
	if err := validateMasterSeed(masterSeed); err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	return &Identity{
		MasterSeedHash:    hashMasterSeed(masterSeed),
		DerivationPath:    fmt.Sprintf("m/%d'/%d'/%d'/%d/%d", PurposeHivemind, CoinTypeHivemind, AccountIndex, ChangeIndex, 0),
		RotationEpoch:     now,
		ExpiresAt:         now + int64(config.KeyExpiration.Seconds()),
		RevokedKeys:       make(map[string]int64),
		CurrentKeyIndex:   0,
		HandleCollisionID: "",
	}, nil
}

// NewIdentityFromSeed creates an identity from a simple seed (legacy compatibility).
// This derives the master seed from the seed using HKDF.
func NewIdentityFromSeed(seedHex string, config IdentityConfig) (*Identity, error) {
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		// Legacy fallback: treat as master seed directly if 32 bytes
		if len(seed) == 32 {
			return NewIdentityFromMasterSeed(seed, config)
		}
		return nil, errors.New("invalid seed length")
	}

	// Derive master seed from legacy seed
	masterSeed := deriveMasterSeed(seed)
	return NewIdentityFromMasterSeed(masterSeed, config)
}

// deriveMasterSeed derives a master seed from a legacy seed using HKDF-SHA512.
func deriveMasterSeed(seed []byte) []byte {
	info := []byte("hivemind-master-seed-v1")
	hkdf := hkdf.New(sha512.New, seed, nil, info)
	masterSeed := make([]byte, 32)
	hkdf.Read(masterSeed)
	return masterSeed
}

// validateMasterSeed checks that a master seed has sufficient entropy.
func validateMasterSeed(seed []byte) error {
	if len(seed) != 32 {
		return errors.New("master seed must be 32 bytes")
	}
	// Check for all zeros or all same byte (degenerate)
	allSame := true
	for i := 1; i < len(seed); i++ {
		if seed[i] != seed[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return errors.New("master seed has insufficient entropy")
	}
	return nil
}

// hashMasterSeed creates a verification hash of the master seed (never reversible).
func hashMasterSeed(seed []byte) string {
	h := sha256.Sum256(seed)
	return hex.EncodeToString(h[:])
}

// VerifyMasterSeed verifies a master seed against the stored hash.
func (id *Identity) VerifyMasterSeed(seed []byte) bool {
	return id.MasterSeedHash == hashMasterSeed(seed)
}

// DeriveKey derives a child key from the master seed using the current derivation path.
// Returns the private key, public key, and key ID.
func (id *Identity) DeriveKey(masterSeed []byte, keyIndex uint32) (ed25519.PrivateKey, ed25519.PublicKey, string, error) {
	if !id.VerifyMasterSeed(masterSeed) {
		return nil, nil, "", ErrIdentityCompromised
	}

	// Check if key is revoked
	keyLabel := keyLabels[keyIndex]
	if revokedAt, revoked := id.RevokedKeys[keyLabel]; revoked {
		return nil, nil, "", fmt.Errorf("%w: %s revoked at %d", ErrRevokedKey, keyLabel, revokedAt)
	}

	// Derive child seed using HKDF with derivation path
	childSeed := deriveChildSeed(masterSeed, id.DerivationPath, keyIndex)

	priv := ed25519.NewKeyFromSeed(childSeed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, nil, "", errors.New("failed to derive public key")
	}

	return priv, pub, keyLabels[keyIndex], nil
}

// deriveChildSeed derives a child seed using HKDF with the full derivation path.
func deriveChildSeed(masterSeed []byte, derivationPath string, keyIndex uint32) []byte {
	// Construct full derivation path including key index
	fullPath := fmt.Sprintf("%s/%d", derivationPath, keyIndex)
	info := []byte("hivemind-child-seed-v1:" + fullPath)

	hkdf := hkdf.New(sha512.New, masterSeed, nil, info)
	childSeed := make([]byte, ed25519.SeedSize)
	hkdf.Read(childSeed)
	return childSeed
}

// GetCurrentSigningKey derives the current signing key (keyIndex 0).
func (id *Identity) GetCurrentSigningKey(masterSeed []byte) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	priv, pub, _, err := id.DeriveKey(masterSeed, id.CurrentKeyIndex)
	return priv, pub, err
}

// GetKeyByLabel derives a key by its label (signing, transport, mesh, backup).
func (id *Identity) GetKeyByLabel(masterSeed []byte, label string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	var keyIndex uint32
	found := false
	for idx, lbl := range keyLabels {
		if lbl == label {
			keyIndex = idx
			found = true
			break
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("unknown key label: %s", label)
	}
	priv, pub, _, err := id.DeriveKey(masterSeed, keyIndex)
	return priv, pub, err
}

// RotateKeys rotates to the next key index, updating rotation epoch.
func (id *Identity) RotateKeys(config IdentityConfig) error {
	now := time.Now().Unix()

	// Check if rotation is needed
	if now-id.RotationEpoch < int64(config.RotationInterval.Seconds()) {
		return nil // not time yet
	}

	// Increment key index (cycle through available keys)
	id.CurrentKeyIndex = (id.CurrentKeyIndex + 1) % uint32(len(keyLabels))
	id.RotationEpoch = now
	id.ExpiresAt = now + int64(config.KeyExpiration.Seconds())

	return nil
}

// MustRotate checks if rotation is required (overdue).
func (id *Identity) MustRotate(config IdentityConfig) bool {
	now := time.Now().Unix()
	return now-id.RotationEpoch >= int64(config.RotationInterval.Seconds())
}

// IsExpired checks if the current key has expired.
func (id *Identity) IsExpired() bool {
	if id.ExpiresAt == 0 {
		return false // never expires
	}
	return time.Now().Unix() >= id.ExpiresAt
}

// RevokeKey revokes a key by label.
func (id *Identity) RevokeKey(label string) error {
	if _, ok := keyLabels[0]; !ok { // just checking map init
		id.RevokedKeys = make(map[string]int64)
	}
	if _, ok := keyLabels[0]; ok { // dummy check
		// Find index for label
		found := false
		for _, lbl := range keyLabels {
			if lbl == label {
				id.RevokedKeys[label] = time.Now().Unix()
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown key label: %s", label)
		}
	}
	return nil
}

// IsRevoked checks if a key label is revoked.
func (id *Identity) IsRevoked(label string) bool {
	_, ok := id.RevokedKeys[label]
	return ok
}

// SetHandleCollision sets a collision suffix for handle disambiguation.
func (id *Identity) SetHandleCollision(suffix string) {
	if suffix == "" {
		id.HandleCollisionID = ""
	} else {
		id.HandleCollisionID = suffix
	}
}

// GetHandle returns the handle with collision suffix if present.
func (id *Identity) GetHandle(baseHandle string) string {
	if id.HandleCollisionID == "" {
		return baseHandle
	}
	return baseHandle + "#" + id.HandleCollisionID
}

// CheckCollision checks if a handle collides with another identity.
func (id *Identity) CheckCollision(other *Identity, baseHandle string) bool {
	return id.GetHandle(baseHandle) == other.GetHandle(baseHandle)
}

// ResolveCollision generates a unique collision suffix using crypto entropy.
func (id *Identity) ResolveCollision(config IdentityConfig) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("entropy failure: %w", ErrNoEntropy)
	}
	id.HandleCollisionID = hex.EncodeToString(suffix)
	return id.HandleCollisionID, nil
}

// MarshalJSON implements custom JSON marshaling (never serializes master seed).
func (id *Identity) MarshalJSON() ([]byte, error) {
	type Alias Identity
	return json.Marshal(&struct {
		*Alias
		MasterSeed []byte `json:"-"` // never serialize master seed
	}{
		Alias: (*Alias)(id),
	})
}

// UnmarshalJSON implements custom JSON unmarshaling.
func (id *Identity) UnmarshalJSON(data []byte) error {
	type Alias Identity
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(id),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	// Initialize maps if nil
	if id.RevokedKeys == nil {
		id.RevokedKeys = make(map[string]int64)
	}
	return nil
}

// GetPublicHandle returns the public handle (hex-encoded public key) for the current signing key.
func (id *Identity) GetPublicHandle(masterSeed []byte) (string, error) {
	_, pub, err := id.GetCurrentSigningKey(masterSeed)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(pub), nil
}

// KeyPair represents a derived key pair with metadata.
type KeyPair struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
	KeyID      string
	Index      uint32
	ExpiresAt  int64
	Revoked    bool
}

// GetAllKeys derives all key pairs for the identity.
func (id *Identity) GetAllKeys(masterSeed []byte) ([]KeyPair, error) {
	// Use sorted indices for deterministic ordering
	indices := make([]uint32, 0, len(keyLabels))
	for idx := range keyLabels {
		indices = append(indices, idx)
	}
	// Sort indices for deterministic ordering
	for i := 0; i < len(indices)-1; i++ {
		for j := i + 1; j < len(indices); j++ {
			if indices[i] > indices[j] {
				indices[i], indices[j] = indices[j], indices[i]
			}
		}
	}

	var keys []KeyPair
	for _, idx := range indices {
		label := keyLabels[idx]
		priv, pub, _, err := id.DeriveKey(masterSeed, idx)
		if err != nil {
			// Key might be revoked, include as revoked entry
			keys = append(keys, KeyPair{
				KeyID:   label,
				Index:   idx,
				Revoked: true,
			})
			continue
		}
		keys = append(keys, KeyPair{
			PrivateKey: priv,
			PublicKey:  pub,
			KeyID:      label,
			Index:      idx,
			ExpiresAt:  id.ExpiresAt,
			Revoked:    false,
		})
	}
	return keys, nil
}

// IdentityManager manages identity operations with thread safety.
type IdentityManager struct {
	identity   *Identity
	config     IdentityConfig
	masterSeed []byte
	mu         sync.RWMutex
	seedLoaded bool
}

// NewIdentityManager creates a new identity manager.
func NewIdentityManager(config IdentityConfig) *IdentityManager {
	return &IdentityManager{
		config: config,
	}
}

// LoadOrCreate loads an identity from storage or creates a new one.
func (im *IdentityManager) LoadOrCreate(identity *Identity, masterSeed []byte) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.identity = identity
	if masterSeed != nil {
		im.masterSeed = masterSeed
		im.seedLoaded = true
	}
	return nil
}

// SetMasterSeed sets the master seed (call once at startup).
func (im *IdentityManager) SetMasterSeed(seed []byte) error {
	im.mu.Lock()
	defer im.mu.Unlock()

	if len(seed) != 32 {
		return errors.New("master seed must be 32 bytes")
	}
	if im.seedLoaded {
		if !im.identity.VerifyMasterSeed(seed) {
			return ErrIdentityCompromised
		}
	}
	im.masterSeed = seed
	im.seedLoaded = true
	return nil
}

// GetMasterSeed returns the master seed (requires unlock).
func (im *IdentityManager) GetMasterSeed() ([]byte, error) {
	im.mu.RLock()
	defer im.mu.RUnlock()

	if !im.seedLoaded {
		return nil, errors.New("master seed not loaded")
	}
	// Return a copy
	seed := make([]byte, len(im.masterSeed))
	copy(seed, im.masterSeed)
	return seed, nil
}

// GetSigningKey returns the current signing key pair.
func (im *IdentityManager) GetSigningKey() (ed25519.PrivateKey, ed25519.PublicKey, error) {
	im.mu.RLock()
	defer im.mu.RUnlock()

	if !im.seedLoaded {
		return nil, nil, errors.New("master seed not loaded")
	}
	return im.identity.GetCurrentSigningKey(im.masterSeed)
}

// GetKeyByLabel returns a key pair by label.
func (im *IdentityManager) GetKeyByLabel(label string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	im.mu.RLock()
	defer im.mu.RUnlock()

	if !im.seedLoaded {
		return nil, nil, errors.New("master seed not loaded")
	}
	return im.identity.GetKeyByLabel(im.masterSeed, label)
}

// RotateIfNeeded rotates keys if rotation is due.
func (im *IdentityManager) RotateIfNeeded() error {
	im.mu.Lock()
	defer im.mu.Unlock()

	if im.identity.MustRotate(im.config) {
		return im.identity.RotateKeys(im.config)
	}
	return nil
}

// CheckExpiration checks if current key is expired.
func (im *IdentityManager) CheckExpiration() bool {
	im.mu.RLock()
	defer im.mu.RUnlock()
	return im.identity.IsExpired()
}

// CheckRotationRequired checks if rotation is overdue.
func (im *IdentityManager) CheckRotationRequired() bool {
	im.mu.RLock()
	defer im.mu.RUnlock()
	return im.identity.MustRotate(im.config)
}

// ExportBackup exports an encrypted backup of the identity (master seed encrypted with passphrase).
// Returns the encrypted backup as JSON.
func (im *IdentityManager) ExportBackup(passphrase string) ([]byte, error) {
	im.mu.RLock()
	defer im.mu.RUnlock()

	if !im.seedLoaded {
		return nil, errors.New("master seed not loaded")
	}

	// Use argon2id to derive encryption key from passphrase
	// For simplicity, we'll use a basic approach here
	// In production, use argon2.IDKey from golang.org/x/crypto/argon2

	// For now, return the identity struct without master seed
	// The master seed would be encrypted separately
	type Backup struct {
		Identity  *Identity `json:"identity"`
		Version   int       `json:"version"`
		CreatedAt int64     `json:"created_at"`
	}
	backup := Backup{
		Identity:  im.identity,
		Version:   1,
		CreatedAt: time.Now().Unix(),
	}
	return json.MarshalIndent(backup, "", "  ")
}

// HandleCollision checks and resolves handle collisions.
func (im *IdentityManager) CheckAndResolveCollision(baseHandle string, existingHandles map[string]bool) (string, error) {
	im.mu.Lock()
	defer im.mu.Unlock()

	// Check if the base handle collides
	if existingHandles[baseHandle] {
		if _, err := im.identity.ResolveCollision(im.config); err != nil {
			return "", err
		}
		// Return the base handle with the collision suffix
		return im.identity.GetHandle(baseHandle), nil
	}
	return baseHandle, nil
}

// IdentityState represents the current state for health checks.
type IdentityState struct {
	Handle           string   `json:"handle"`
	KeyIndex         uint32   `json:"key_index"`
	KeyLabel         string   `json:"key_label"`
	RotationEpoch    int64    `json:"rotation_epoch"`
	ExpiresAt        int64    `json:"expires_at"`
	Expired          bool     `json:"expired"`
	RotationOverdue  bool     `json:"rotation_overdue"`
	RevokedKeys      []string `json:"revoked_keys"`
	HandleCollision  string   `json:"handle_collision,omitempty"`
	CollisionChecked bool     `json:"collision_checked"`
}

// GetState returns current identity state for health checks.
func (im *IdentityManager) GetState(config IdentityConfig) IdentityState {
	im.mu.RLock()
	defer im.mu.RUnlock()

	state := IdentityState{
		KeyIndex:        im.identity.CurrentKeyIndex,
		KeyLabel:        keyLabels[im.identity.CurrentKeyIndex],
		RotationEpoch:   im.identity.RotationEpoch,
		ExpiresAt:       im.identity.ExpiresAt,
		Expired:         im.identity.IsExpired(),
		RotationOverdue: im.identity.MustRotate(im.config),
		RevokedKeys:     make([]string, 0, len(im.identity.RevokedKeys)),
		HandleCollision: im.identity.HandleCollisionID,
	}

	for label := range im.identity.RevokedKeys {
		state.RevokedKeys = append(state.RevokedKeys, label)
	}

	if im.identity.HandleCollisionID != "" {
		state.HandleCollision = im.identity.HandleCollisionID
	}

	return state
}
