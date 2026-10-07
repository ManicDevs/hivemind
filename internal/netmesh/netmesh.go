package netmesh

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"io"
	"sync"
)

type MeshPacket struct {
	SenderID  string            `json:"sender_id"`
	EventType string            `json:"event_type"`
	Payload   map[string]string `json:"payload"`
}

type MeshTransport struct {
	mu     sync.Mutex
	nodeID string
	key    []byte
	outbox []MeshPacket
}

func NewMeshTransport(nodeID string) *MeshTransport {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &MeshTransport{
		nodeID: nodeID,
		key:    key,
	}
}

func (m *MeshTransport) BroadcastTelemetry(eventType string, data map[string]string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pkt := MeshPacket{
		SenderID:  m.nodeID,
		EventType: eventType,
		Payload:   data,
	}
	m.outbox = append(m.outbox, pkt)

	plain, err := json.Marshal(pkt)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(m.key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	ciphertext := gcm.Seal(nonce, nonce, plain, nil)
	return ciphertext, nil
}
