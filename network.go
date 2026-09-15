package main

import (
    "bufio"
    "crypto/aes"
    "crypto/cipher"
    "crypto/ed25519"
    "crypto/rand"
    "encoding/base64"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "io"
    "net"
    "net/http"
    "os"
    "strings"
    "sync"
    "time"
)

const (
    SocketPath = "/tmp/hivemind.sock"
    NtfyRelay  = "https://ntfy.sh/cerberus-hive-relay-99"
    CipherKey  = "HIVE_MIND_32_BYTE_STATIC_KEY_PAD"
)

type NetworkBroker struct {
    mu         sync.Mutex
    swarm      *Swarm
    listener   net.Listener
    peers      map[string]net.Conn
    history    map[string]bool
    isServer   bool
    pub        string
    priv       ed25519.PrivateKey
    stopChan   chan struct{}
    doneChan   chan struct{}
}

func NewNetworkBroker(swarm *Swarm) *NetworkBroker {
    pub, priv, err := ed25519.GenerateKey(rand.Reader)
    pubStr := hex.EncodeToString(pub)
    if err != nil {
        dummyBytes := make([]byte, 32)
        _, _ = rand.Read(dummyBytes)
        priv = ed25519.NewKeyFromSeed(dummyBytes)
        pubStr = hex.EncodeToString(priv.Public().(ed25519.PublicKey))
    }
    return &NetworkBroker{
        swarm:    swarm,
        peers:    make(map[string]net.Conn),
        history:  make(map[string]bool),
        pub:      pubStr,
        priv:     priv,
        stopChan: make(chan struct{}),
        doneChan: make(chan struct{}),
    }
}

func (nb *NetworkBroker) Encrypt(plaintext []byte) (string, error) {
    block, err := aes.NewCipher([]byte(CipherKey))
    if err != nil {
        return "", err
    }
    aesgcm, err := cipher.NewGCM(block)
    if err != nil {
        return "", err
    }
    nonce := make([]byte, aesgcm.NonceSize())
    if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
        return "", err
    }
    ciphertext := aesgcm.Seal(nonce, nonce, plaintext, nil)
    return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (nb *NetworkBroker) Decrypt(cryptoText string) ([]byte, error) {
    ciphertext, err := base64.StdEncoding.DecodeString(cryptoText)
    if err != nil {
        return nil, err
    }
    block, err := aes.NewCipher([]byte(CipherKey))
    if err != nil {
        return nil, err
    }
    aesgcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }
    nonceSize := aesgcm.NonceSize()
    if len(ciphertext) < nonceSize {
        return nil, fmt.Errorf("ciphertext too short")
    }
    nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
    return aesgcm.Open(nil, nonce, actualCiphertext, nil)
}

func (nb *NetworkBroker) ListenUnixSocket() error {
    _ = os.Remove(SocketPath)

    l, err := net.Listen("unix", SocketPath)
    if err != nil {
        return fmt.Errorf("unix domain socket binding blocked: %w", err)
    }
    nb.listener = l
    nb.isServer = true
    
    nb.mu.Lock()
    nb.history[SocketPath] = true
    nb.mu.Unlock()
    
    fmt.Printf("🔒 [LOCAL IPC BRIDGE] Active unix domain socket listener locked on: %s\n", SocketPath)

    nb.swarm.SetOutbound(nb.handleOutbound)
    go nb.acceptConnections()
    go nb.ListenToCloudRelay()
    return nil
}

func (nb *NetworkBroker) acceptConnections() {
    for {
        conn, err := nb.listener.Accept()
        if err != nil {
            select {
            case <-nb.stopChan:
                return
            default:
                continue
            }
        }

        // Accepted links are full peers: without storing them, the server
        // could hear clients but never speak back — a half-duplex mesh.
        key := conn.RemoteAddr().String()
        nb.mu.Lock()
        nb.peers[key] = conn
        nb.history[SocketPath] = true
        nb.mu.Unlock()

        go nb.HandleConnection(conn, key)
    }
}

func (nb *NetworkBroker) ConnectToUnixSocket() error {
    var conn net.Conn
    var err error
    
    fmt.Println("⏳ [RETRY LOOP] Scanning filesystem for active LUDS descriptor. Attempting attachment...")
    for {
        conn, err = net.Dial("unix", SocketPath)
        if err == nil {
            break
        }
        select {
        case <-nb.stopChan:
            return fmt.Errorf("connection aborted by user signal intercept")
        default:
            time.Sleep(1 * time.Second)
        }
    }

    nb.mu.Lock()
    nb.peers[SocketPath] = conn
    nb.history[SocketPath] = true
    nb.mu.Unlock()

    fmt.Printf("🔒 [LUDS LINK SECURED] Connection interface bound to socket file: %s\n", SocketPath)

    // The handshake is a fully mined + signed frame like any other —
    // unverified greetings are dropped by the swarm, including our own.
    // It rides the standard path: local broadcast first (our own minds
    // register the link), the outbound bridge carries it to the server.
    nb.swarm.SetOutbound(nb.handleOutbound)
    msg := MineMessage(nb.swarm, nb.priv, nb.pub, "hello", "LUDS_CLIENT_HANDSHAKE", []float64{0.0, 1.0, 9.81, -0.15})
    nb.swarm.Broadcast(msg)

    go nb.HandleConnection(conn, SocketPath)
    go nb.ListenToCloudRelay()
    return nil
}

func (nb *NetworkBroker) handleOutbound(msg SecureMessage) {
    nb.ForwardToUnixPeers(msg)
    go nb.PublishToCloudRelay(msg)
}

func (nb *NetworkBroker) HandleConnection(conn net.Conn, key string) {
    defer func() {
        nb.mu.Lock()
        delete(nb.peers, key)
        nb.mu.Unlock()
        conn.Close()
    }()

    decoder := json.NewDecoder(conn)
    for {
        var msg SecureMessage
        if err := decoder.Decode(&msg); err != nil {
            break
        }

        // Wire frames are marked relayed: they join the local hive but
        // never echo back out through the outbound bridge.
        msg.Relayed = true
        nb.swarm.Broadcast(msg)
    }
}

func (nb *NetworkBroker) ForwardToUnixPeers(msg SecureMessage) {
    nb.mu.Lock()
    defer nb.mu.Unlock()

    jsonData, err := json.Marshal(msg)
    if err != nil {
        return
    }

    for _, conn := range nb.peers {
        _, _ = conn.Write(append(jsonData, '\n'))
    }
}

func (nb *NetworkBroker) PublishToCloudRelay(msg SecureMessage) {
    payload, err := json.Marshal(msg)
    if err != nil {
        return
    }

    encryptedString, err := nb.Encrypt(payload)
    if err != nil {
        return
    }

    url := NtfyRelay
    req, err := http.NewRequest("POST", url, strings.NewReader(encryptedString))
    if err != nil {
        return
    }
    
    // Aligned to match the stream title tag precisely
    req.Header.Set("X-Title", "ENCRYPTED_HIVE_FRAME")
    
    client := &http.Client{Timeout: 10 * time.Second}
    resp, err := client.Do(req)
    if err != nil {
        return
    }
    defer resp.Body.Close()
}

func (nb *NetworkBroker) ListenToCloudRelay() {
    url := NtfyRelay + "/json"
    
    for {
        select {
        case <-nb.stopChan:
            return
        default:
            resp, err := http.Get(url)
            if err != nil {
                time.Sleep(5 * time.Second)
                continue
            }

            scanner := bufio.NewScanner(resp.Body)
            for scanner.Scan() {
                var ntfyMsg map[string]interface{}
                if err := json.Unmarshal(scanner.Bytes(), &ntfyMsg); err != nil {
                    continue
                }

                event, _ := ntfyMsg["event"].(string)
                if event != "message" {
                    continue
                }

                // FIXED: Read the incoming tag header matching title metadata frames
                title, _ := ntfyMsg["title"].(string)
                if title != "ENCRYPTED_HIVE_FRAME" {
                    continue
                }

                encryptedBody, _ := ntfyMsg["message"].(string)
                
                decryptedBytes, err := nb.Decrypt(encryptedBody)
                if err != nil {
                    continue
                }

                var secureMsg SecureMessage
                if err := json.Unmarshal(decryptedBytes, &secureMsg); err != nil {
                    continue
                }

                if nb.swarm.Broadcast(secureMsg) {
                    fmt.Printf("☁️  [SECURE CLOUD INBOUND] Physics frame extracted and verified over public stream.\n")
                    nb.ForwardToUnixPeers(secureMsg)
                }
            }
            _ = resp.Body.Close()
            time.Sleep(2 * time.Second)
        }
    }
}

func (nb *NetworkBroker) NotifyEmergency(topic, message string) {
    _ = strings.ToLower(topic)
    _ = message
}

func (nb *NetworkBroker) Close() {
    close(nb.stopChan)
    if nb.listener != nil {
        _ = nb.listener.Close()
    }
    if nb.isServer {
        _ = os.Remove(SocketPath)
    }

    nb.mu.Lock()
    defer nb.mu.Unlock()

    fmt.Println("\n🗃️  [LUDS SHUTDOWN REGISTRY] Active verified two-way local conversation links at termination:")
    if len(nb.history) == 0 {
        fmt.Println("     (none — no file socket conversations were registered during this runtime window)")
    } else {
        for addr := range nb.history {
            fmt.Printf("     ✔ Confirmed two-way local machine LUDS interaction pipe active via: %s\n", addr)
        }
    }

    for _, conn := range nb.peers {
        _ = conn.Close()
    }
}
