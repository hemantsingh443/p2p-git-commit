package p2p

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/multiformats/go-multiaddr"
)

// CreateHost creates a new libp2p host with NAT traversal capabilities.
// It now accepts a private key to ensure a persistent identity.
func CreateHost(ctx context.Context, privKey crypto.PrivKey, listenPort int) (host.Host, error) {
	// 0.0.0.0 listens on all available interfaces.
	listenAddr := multiaddr.StringCast(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", listenPort))

	// libp2p.New constructs a new libp2p Host.
	// Other options can be added here.
	h, err := libp2p.New(
		libp2p.Identity(privKey), // Use the provided private key for a persistent ID
		libp2p.ListenAddrs(listenAddr),
		libp2p.NATPortMap(),         // Attempt to open a port in the NAT for us.
		libp2p.EnableHolePunching(), // Enable NAT traversal
		libp2p.EnableRelay(),        // Enable relay capabilities
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	fmt.Printf("Host created with ID: %s\n", h.ID())
	return h, nil
}

// StartDiscovery connects to the public IPFS/libp2p bootstrap nodes to join the DHT
// and discover other peers. This is crucial for NAT traversal and finding peers
// on the public internet.
func StartDiscovery(ctx context.Context, h host.Host) error {
	// Create a new DHT
	kademliaDHT, err := dht.New(ctx, h)
	if err != nil {
		return fmt.Errorf("failed to create DHT: %w", err)
	}

	// Bootstrap the DHT. In the default configuration, this connects to
	// public IPFS bootstrap nodes.
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		return fmt.Errorf("failed to bootstrap DHT: %w", err)
	}

	// Announce ourselves so other peers can find us
	routingDiscovery := routing.NewRoutingDiscovery(kademliaDHT)
	routingDiscovery.Advertise(ctx, "/p2p-git-remote/1.0.0")

	// Now, look for others
	_, err = routingDiscovery.FindPeers(ctx, "/p2p-git-remote/1.0.0")
	if err != nil {
		return fmt.Errorf("failed to find peers: %w", err)
	}

	// Connect to bootstrap peers to improve connectivity
	for _, addr := range dht.DefaultBootstrapPeers {
		pi, _ := peer.AddrInfoFromP2pAddr(addr)
		// We ignore errors as some bootstrap nodes may be down
		h.Connect(ctx, *pi)
	}

	return nil
}
