package store

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// TrustStore manages a list of trusted peer IDs.
type TrustStore struct {
	path         string
	trustedPeers map[peer.ID]bool
	mutex        sync.RWMutex
}

// NewTrustStore creates a new TrustStore, loading from the given file path.
func NewTrustStore(path string) (*TrustStore, error) {
	ts := &TrustStore{
		path:         path,
		trustedPeers: make(map[peer.ID]bool),
	}
	if err := ts.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return ts, nil
}

// IsTrusted checks if a peer is in the trust store.
func (ts *TrustStore) IsTrusted(p peer.ID) bool {
	ts.mutex.RLock()
	defer ts.mutex.RUnlock()
	return ts.trustedPeers[p]
}

// AddTrustedPeer adds a peer to the trust store and saves to disk.
func (ts *TrustStore) AddTrustedPeer(p peer.ID) error {
	ts.mutex.Lock()
	defer ts.mutex.Unlock()
	ts.trustedPeers[p] = true
	return ts.save()
}

func (ts *TrustStore) load() error {
	data, err := os.ReadFile(ts.path)
	if err != nil {
		return err
	}
	var peers []string
	if err := json.Unmarshal(data, &peers); err != nil {
		return err
	}
	for _, pStr := range peers {
		p, err := peer.Decode(pStr)
		if err != nil {
			// Skip invalid entries
			continue
		}
		ts.trustedPeers[p] = true
	}
	return nil
}

func (ts *TrustStore) save() error {
	var peers []string
	for p := range ts.trustedPeers {
		peers = append(peers, p.String())
	}
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ts.path, data, 0644)
}
