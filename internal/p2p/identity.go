package p2p

import (
	"crypto/rand"
	"fmt"
	"os"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// LoadOrGeneratePrivateKey loads a private key from a file, or generates a new one.
func LoadOrGeneratePrivateKey(path string) (crypto.PrivKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// If the file doesn't exist, generate a new key
		if os.IsNotExist(err) {
			fmt.Printf("Generating new private key at %s\n", path)
			privKey, _, err := crypto.GenerateKeyPairWithReader(crypto.Ed25519, -1, rand.Reader)
			if err != nil {
				return nil, fmt.Errorf("failed to generate key pair: %w", err)
			}

			keyBytes, err := crypto.MarshalPrivateKey(privKey)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal private key: %w", err)
			}

			if err := os.WriteFile(path, keyBytes, 0600); err != nil {
				return nil, fmt.Errorf("failed to write key file: %w", err)
			}
			return privKey, nil
		}
		return nil, fmt.Errorf("failed to read key file: %w", err)
	}

	// If the file exists, unmarshal it
	privKey, err := crypto.UnmarshalPrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal private key: %w", err)
	}
	fmt.Printf("Loaded private key from %s\n", path)
	return privKey, nil
}
