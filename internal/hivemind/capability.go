package hivemind

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

var (
	ErrInvalidToken      = errors.New("invalid token")
	ErrTokenExpired      = errors.New("token expired")
	ErrInsufficientScope = errors.New("insufficient scope")
	ErrTokenRevoked      = errors.New("token revoked")
	ErrInvalidIssuer     = errors.New("invalid issuer")
	ErrInvalidAudience   = errors.New("invalid audience")
)

type CapabilityConfig struct {
	Issuer              string
	DefaultTTL          time.Duration
	MaxTTL              time.Duration
	SigningKey          ed25519.PrivateKey
	VerificationKey     ed25519.PublicKey
	ClockSkew           time.Duration
	RevocationCheck     bool
	RevocationListTTL   time.Duration
}

func DefaultCapabilityConfig() CapabilityConfig {
	return CapabilityConfig{
		Issuer:            "hivemind",
		DefaultTTL:        1 * time.Hour,
		MaxTTL:            24 * time.Hour,
		ClockSkew:         30 * time.Second,
		RevocationCheck:   true,
		RevocationListTTL: 5 * time.Minute,
	}
}

type Capability string

const (
	CapMeshJoin       Capability = "mesh:join"
	CapMeshLeave      Capability = "mesh:leave"
	CapFrameSend      Capability = "frame:send"
	CapFrameReceive   Capability = "frame:receive"
	CapRelayPublish   Capability = "relay:publish"
	CapRelaySubscribe Capability = "relay:subscribe"
	CapSoulRead       Capability = "soul:read"
	CapSoulWrite      Capability = "soul:write"
	CapSoulDelete     Capability = "soul:delete"
	CapAdmin          Capability = "admin:*"
	CapSupernode      Capability = "supernode:announce"
	CapDHT            Capability = "dht:*"
	CapPunch          Capability = "punch:request"
	CapCompute        Capability = "compute:*"
	CapWASM           Capability = "wasm:load"
	CapStorage        Capability = "storage:*"
)

type Token struct {
	ID            string                 `json:"jti"`
	Subject       string                 `json:"sub"`
	Issuer        string                 `json:"iss"`
	Audience      string                 `json:"aud"`
	IssuedAt      int64                  `json:"iat"`
	ExpiresAt     int64                  `json:"exp"`
	NotBefore     int64                  `json:"nbf"`
	Capabilities  []Capability           `json:"cap"`
	Constraints   map[string]interface{} `json:"cnf,omitempty"`
	Nonce         string                 `json:"nonce,omitempty"`
}

type CapabilityManager struct {
	mu            sync.RWMutex
	config        CapabilityConfig
	revoked       map[string]time.Time
	revokedNonces map[string]bool
	issuedTokens  map[string]*Token
	stopChan      chan struct{}
	cleanupTicker *time.Ticker
}

func NewCapabilityManager(config CapabilityConfig) (*CapabilityManager, error) {
	if config.SigningKey == nil {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("failed to generate signing key: %w", err)
		}
		config.SigningKey = priv
		config.VerificationKey = pub
	}

	cm := &CapabilityManager{
		config:         config,
		revoked:        make(map[string]time.Time),
		revokedNonces:  make(map[string]bool),
		issuedTokens:   make(map[string]*Token),
		stopChan:       make(chan struct{}),
	}

	cm.cleanupTicker = time.NewTicker(config.RevocationListTTL)
	go cm.cleanupLoop()

	return cm, nil
}

func (cm *CapabilityManager) IssueToken(ctx context.Context, subject string, capabilities []Capability, ttl time.Duration, audience string, constraints map[string]interface{}) (string, error) {
	if ttl > cm.config.MaxTTL {
		ttl = cm.config.MaxTTL
	}
	if ttl <= 0 {
		ttl = cm.config.DefaultTTL
	}

	now := time.Now()
	expiresAt := now.Add(ttl)

	tokenID := generateTokenID()
	nonce := generateNonce()

	token := &Token{
		ID:            tokenID,
		Subject:       subject,
		Issuer:        cm.config.Issuer,
		Audience:      audience,
		IssuedAt:      now.Unix(),
		ExpiresAt:     expiresAt.Unix(),
		NotBefore:     now.Unix(),
		Capabilities:  capabilities,
		Constraints:   constraints,
		Nonce:         nonce,
	}

	cm.mu.Lock()
	cm.issuedTokens[tokenID] = token
	cm.revokedNonces[nonce] = false
	cm.mu.Unlock()

	jwtToken := jwt.New()
	jwtToken.Set(jwt.SubjectKey, subject)
	jwtToken.Set(jwt.IssuerKey, cm.config.Issuer)
	jwtToken.Set(jwt.AudienceKey, audience)
	jwtToken.Set(jwt.IssuedAtKey, now)
	jwtToken.Set(jwt.ExpirationKey, expiresAt)
	jwtToken.Set(jwt.NotBeforeKey, now)
	jwtToken.Set("jti", tokenID)
	jwtToken.Set("cap", capabilities)
	jwtToken.Set("cnf", constraints)
	jwtToken.Set("nonce", nonce)

	// Marshal token to JSON for signing
	payload, err := json.Marshal(jwtToken)
	if err != nil {
		return "", fmt.Errorf("failed to marshal token: %w", err)
	}

	signed, err := jws.Sign(payload, jws.WithKey(jwa.EdDSA, cm.config.SigningKey))
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return string(signed), nil
}

func (cm *CapabilityManager) VerifyToken(ctx context.Context, tokenString string, audience string) (*Token, error) {
	tok, err := jwt.ParseString(tokenString,
		jwt.WithKey(jwa.EdDSA, cm.config.VerificationKey),
		jwt.WithIssuer(cm.config.Issuer),
		jwt.WithAudience(audience),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	// Validate token with acceptable skew
	if err := jwt.Validate(tok,
		jwt.WithAcceptableSkew(cm.config.ClockSkew),
	); err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	token := &Token{
		ID:        tok.JwtID(),
		Subject:   tok.Subject(),
		Issuer:    tok.Issuer(),
		Audience:  tok.Audience()[0],
		IssuedAt:  tok.IssuedAt().Unix(),
		ExpiresAt: tok.Expiration().Unix(),
		NotBefore: tok.NotBefore().Unix(),
	}

	if caps, ok := tok.Get("cap"); ok {
		if capSlice, ok := caps.([]interface{}); ok {
			for _, c := range capSlice {
				if capStr, ok := c.(string); ok {
					token.Capabilities = append(token.Capabilities, Capability(capStr))
				}
			}
		}
	}

	if cnf, ok := tok.Get("cnf"); ok {
		if constraints, ok := cnf.(map[string]interface{}); ok {
			token.Constraints = constraints
		}
	}

	if nonce, ok := tok.Get("nonce"); ok {
		if nonceStr, ok := nonce.(string); ok {
			token.Nonce = nonceStr
		}
	}

	cm.mu.RLock()
	if _, revoked := cm.revoked[token.ID]; revoked {
		cm.mu.RUnlock()
		return nil, ErrTokenRevoked
	}
	if token.Nonce != "" {
		if revoked, ok := cm.revokedNonces[token.Nonce]; ok && revoked {
			cm.mu.RUnlock()
			return nil, ErrTokenRevoked
		}
	}
	cm.mu.RUnlock()

	if time.Now().Unix() > token.ExpiresAt {
		return nil, ErrTokenExpired
	}

	if time.Now().Unix() < token.NotBefore {
		return nil, errors.New("token not yet valid")
	}

	return token, nil
}

func (cm *CapabilityManager) HasCapability(token *Token, cap Capability) bool {
	for _, c := range token.Capabilities {
		if c == cap || matchesWildcard(c, cap) {
			return true
		}
	}
	return false
}

func matchesWildcard(pattern, target Capability) bool {
	p := string(pattern)
	if strings.HasSuffix(p, "*") {
		prefix := strings.TrimSuffix(p, "*")
		return strings.HasPrefix(string(target), prefix)
	}
	return false
}

func (cm *CapabilityManager) RevokeToken(tokenID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.revoked[tokenID] = time.Now()
	delete(cm.issuedTokens, tokenID)
	return nil
}

func (cm *CapabilityManager) RevokeNonce(nonce string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.revokedNonces[nonce] = true
	return nil
}

func (cm *CapabilityManager) RevokeAllForSubject(subject string) int {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	count := 0
	for id, token := range cm.issuedTokens {
		if token.Subject == subject {
			cm.revoked[id] = time.Now()
			delete(cm.issuedTokens, id)
			count++
		}
	}
	return count
}

func (cm *CapabilityManager) IsRevoked(tokenID string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	_, ok := cm.revoked[tokenID]
	return ok
}

func (cm *CapabilityManager) IntrospectToken(tokenString string) (*Token, error) {
	return cm.VerifyToken(context.Background(), tokenString, "")
}

func (cm *CapabilityManager) GetTokenInfo(tokenID string) (*Token, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	token, ok := cm.issuedTokens[tokenID]
	return token, ok
}

func (cm *CapabilityManager) ListActiveTokens(subject string) []*Token {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	var tokens []*Token
	for _, token := range cm.issuedTokens {
		if subject == "" || token.Subject == subject {
			if time.Now().Unix() < token.ExpiresAt {
				tokens = append(tokens, token)
			}
		}
	}
	return tokens
}

func (cm *CapabilityManager) cleanupLoop() {
	for {
		select {
		case <-cm.stopChan:
			return
		case <-cm.cleanupTicker.C:
			cm.cleanup()
		}
	}
}

func (cm *CapabilityManager) cleanup() {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	now := time.Now().Unix()
	for id, token := range cm.issuedTokens {
		if now > token.ExpiresAt {
			delete(cm.issuedTokens, id)
		}
	}

	for id, revokedAt := range cm.revoked {
		if time.Since(revokedAt) > cm.config.RevocationListTTL*10 {
			delete(cm.revoked, id)
		}
	}

	for nonce, revoked := range cm.revokedNonces {
		if revoked {
			delete(cm.revokedNonces, nonce)
		}
	}
}

func (cm *CapabilityManager) GetStats() map[string]interface{} {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	active := 0
	revoked := len(cm.revoked)
	for _, token := range cm.issuedTokens {
		if time.Now().Unix() < token.ExpiresAt {
			active++
		}
	}

	return map[string]interface{}{
		"active_tokens":  active,
		"revoked_tokens": revoked,
		"revoked_nonces": len(cm.revokedNonces),
		"issuer":         cm.config.Issuer,
	}
}

func (cm *CapabilityManager) Stop() {
	close(cm.stopChan)
	if cm.cleanupTicker != nil {
		cm.cleanupTicker.Stop()
	}
}

type DelegationToken struct {
	Token          string    `json:"token"`
	Delegator      string    `json:"delegator"`
	Delegatee      string    `json:"delegatee"`
	OriginalCaps   []Capability `json:"original_caps"`
	DelegatedCaps  []Capability `json:"delegated_caps"`
	ExpiresAt      time.Time `json:"expires_at"`
	CreatedAt      time.Time `json:"created_at"`
	Revoked        bool      `json:"revoked"`
}

type DelegationManager struct {
	mu            sync.RWMutex
	delegations   map[string]*DelegationToken
	byDelegator   map[string][]string
	byDelegatee   map[string][]string
	capManager    *CapabilityManager
	stopChan      chan struct{}
}

func NewDelegationManager(capManager *CapabilityManager) *DelegationManager {
	return &DelegationManager{
		delegations: make(map[string]*DelegationToken),
		byDelegator: make(map[string][]string),
		byDelegatee: make(map[string][]string),
		capManager:  capManager,
		stopChan:    make(chan struct{}),
	}
}

func (dm *DelegationManager) Delegate(ctx context.Context, delegator, delegatee string, capabilities []Capability, ttl time.Duration, constraints map[string]interface{}) (*DelegationToken, error) {
	delegatorToken, err := dm.getDelegatorToken(delegator)
	if err != nil {
		return nil, err
	}

	for _, cap := range capabilities {
		if !dm.capManager.HasCapability(delegatorToken, cap) {
			return nil, fmt.Errorf("delegator lacks capability: %s", cap)
		}
	}

	delegationID := generateTokenID()
	now := time.Now()
	expiresAt := now.Add(ttl)

	delegation := &DelegationToken{
		Token:          generateTokenID(),
		Delegator:      delegator,
		Delegatee:      delegatee,
		OriginalCaps:   delegatorToken.Capabilities,
		DelegatedCaps:  capabilities,
		ExpiresAt:      expiresAt,
		CreatedAt:      now,
		Revoked:        false,
	}

	dm.mu.Lock()
	dm.delegations[delegationID] = delegation
	dm.byDelegator[delegator] = append(dm.byDelegator[delegator], delegationID)
	dm.byDelegatee[delegatee] = append(dm.byDelegatee[delegatee], delegationID)
	dm.mu.Unlock()

	tokenString, err := dm.capManager.IssueToken(ctx, delegatee, capabilities, ttl, "", nil)
	if err != nil {
		return nil, err
	}

	delegation.Token = tokenString
	return delegation, nil
}

func (dm *DelegationManager) RevokeDelegation(delegationID string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	delegation, ok := dm.delegations[delegationID]
	if !ok {
		return errors.New("delegation not found")
	}

	delegation.Revoked = true
	delegation.ExpiresAt = time.Now()
	return nil
}

func (dm *DelegationManager) GetDelegationsForDelegatee(delegatee string) []*DelegationToken {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	ids, ok := dm.byDelegatee[delegatee]
	if !ok {
		return nil
	}

	var delegations []*DelegationToken
	for _, id := range ids {
		if d, ok := dm.delegations[id]; ok && !d.Revoked && time.Now().Before(d.ExpiresAt) {
			delegations = append(delegations, d)
		}
	}
	return delegations
}

func (dm *DelegationManager) GetDelegationsForDelegator(delegator string) []*DelegationToken {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	ids, ok := dm.byDelegator[delegator]
	if !ok {
		return nil
	}

	var delegations []*DelegationToken
	for _, id := range ids {
		if d, ok := dm.delegations[id]; ok {
			delegations = append(delegations, d)
		}
	}
	return delegations
}

func (dm *DelegationManager) getDelegatorToken(subject string) (*Token, error) {
	return &Token{
		Subject:       subject,
		Capabilities:  []Capability{CapAdmin},
		ExpiresAt:     time.Now().Add(24 * time.Hour).Unix(),
	}, nil
}

func (dm *DelegationManager) GetStats() map[string]interface{} {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	active := 0
	for _, d := range dm.delegations {
		if !d.Revoked && time.Now().Before(d.ExpiresAt) {
			active++
		}
	}

	return map[string]interface{}{
		"total_delegations": len(dm.delegations),
		"active_delegations": active,
	}
}

func (dm *DelegationManager) Stop() {
	close(dm.stopChan)
}

func generateTokenID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func generateNonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}