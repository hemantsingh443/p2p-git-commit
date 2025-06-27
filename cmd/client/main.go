package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"

	p2p "github.com/hemantsingh443/p2p-git-remote/internal/p2p"
	"github.com/hemantsingh443/p2p-git-remote/internal/protocol"
	"github.com/hemantsingh443/p2p-git-remote/internal/store"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

func main() {
	daemonAddr := flag.String("d", "", "Daemon's multiaddress (from QR code)")
	repoAlias := flag.String("repo", "", "Alias of the repository to commit to")
	commitMsg := flag.String("m", "", "Commit message")
	branch := flag.String("b", "main", "The branch to push to (defaults to 'main')")
	listRepos := flag.Bool("list-repos", false, "List available repos on the daemon")
	readFile := flag.String("read-file", "", "Read a file from a repo (format: repo_alias:path/to/file)")
	flag.Parse()

	if *daemonAddr == "" {
		log.Fatal("Please provide the daemon's multiaddress using the -d flag.")
	}

	isCommitCommand := *repoAlias != "" && *commitMsg != ""
	if *repoAlias != "" && *commitMsg == "" {
		log.Fatal("A commit message (-m) is required when specifying a repo (-repo).")
	}

	ctx := context.Background()

	// --- NEW: Load or generate persistent identity ---
	privKey, err := p2p.LoadOrGeneratePrivateKey("client_identity.key")
	if err != nil {
		log.Fatalf("Failed to get private key: %v", err)
	}

	// --- MODIFIED: Pass the key to CreateHost ---
	h, err := p2p.CreateHost(ctx, privKey, 0)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	// We still run discovery in the background to help with NAT traversal
	go func() {
		if err := p2p.StartDiscovery(ctx, h); err != nil {
			log.Printf("Warning: Discovery failed: %v", err)
		}
	}()

	maddr, err := multiaddr.NewMultiaddr(*daemonAddr)
	if err != nil {
		log.Fatalf("Invalid multiaddress: %v", err)
	}

	addrInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		log.Fatalf("Failed to parse peer address info: %v", err)
	}

	// Load our client's trust store to see if we already trust this daemon
	trustStore, err := store.NewTrustStore("client_trusted_daemon.json")
	if err != nil {
		log.Fatalf("Failed to open client trust store: %v", err)
	}

	// Always connect first
	if err := h.Connect(ctx, *addrInfo); err != nil {
		log.Fatalf("Failed to connect to daemon: %v", err)
	}
	fmt.Println("Connection to daemon established.")

	// --- NEW: Handle list-repos and read-file flags ---
	if *listRepos {
		sendListReposRequest(ctx, h, *addrInfo)
		return
	} else if *readFile != "" {
		// parse the readFile flag and call sendReadFileRequest
		parts := strings.SplitN(*readFile, ":", 2)
		if len(parts) != 2 {
			log.Fatalf("Invalid format for --read-file. Use repo_alias:path/to/file")
		}
		sendReadFileRequest(ctx, h, *addrInfo, parts[0], parts[1])
		return
	}

	// --- RESTRUCTURED WORKFLOW ---
	if isCommitCommand {
		// If we are sending a command, we must already be trusted.
		if !trustStore.IsTrusted(addrInfo.ID) {
			log.Fatalf("Cannot send command: Daemon %s is not in our trust store. Please connect once without flags to perform handshake.", addrInfo.ID)
		}
		sendCommitRequest(ctx, h, *addrInfo, *repoAlias, *commitMsg, *branch)
	} else {
		// If not sending a command, our goal is to check trust and handshake if needed.
		if trustStore.IsTrusted(addrInfo.ID) {
			fmt.Println("Daemon is already trusted. Ready to send commands.")
		} else {
			performHandshake(ctx, h, *addrInfo, trustStore)
		}
	}
}

func performHandshake(ctx context.Context, h host.Host, addrInfo peer.AddrInfo, ts *store.TrustStore) {
	fmt.Println("Performing first-time handshake...")
	stream, err := h.NewStream(ctx, addrInfo.ID, protocol.ProtocolID)
	if err != nil {
		log.Fatalf("Failed to open stream for handshake: %v", err)
	}
	defer stream.Close()

	handshakeReq := &protocol.Message{Type: "HANDSHAKE_REQUEST"}
	if err := protocol.WriteMessage(stream, handshakeReq); err != nil {
		log.Fatalf("Failed to send handshake: %v", err)
	}

	response, err := protocol.ReadMessage(stream)
	if err != nil {
		log.Fatalf("Failed to read handshake response: %v", err)
	}

	var payload protocol.HandshakeResponsePayload
	if err := json.Unmarshal(response.Payload, &payload); err != nil {
		log.Fatalf("Failed to parse handshake payload: %v", err)
	}

	if payload.Approved {
		fmt.Println("Handshake successful! Daemon approved us.")
		ts.AddTrustedPeer(addrInfo.ID)
	} else {
		log.Println("Handshake failed. Daemon rejected the connection.")
	}
}

func sendCommitRequest(ctx context.Context, h host.Host, addrInfo peer.AddrInfo, repo, msg, branch string) {
	fmt.Printf("Sending git commit request for repo '%s'\n", repo)
	stream, err := h.NewStream(ctx, addrInfo.ID, protocol.ProtocolID)
	if err != nil {
		log.Fatalf("Failed to open stream for git command: %v", err)
	}
	defer stream.Close()

	gitPayload := protocol.GitCommitRequestPayload{
		RepoPath: repo,
		Message:  msg,
		Branch:   branch,
	}
	payloadBytes, _ := json.Marshal(gitPayload)
	gitReq := &protocol.Message{Type: "GIT_COMMIT_REQUEST", Payload: payloadBytes}

	if err := protocol.WriteMessage(stream, gitReq); err != nil {
		log.Fatalf("Failed to send git request: %v", err)
	}

	gitResponse, err := protocol.ReadMessage(stream)
	if err != nil {
		log.Fatalf("Failed to read git response: %v", err)
	}

	var respPayload protocol.GitCommitResponsePayload
	if err := json.Unmarshal(gitResponse.Payload, &respPayload); err != nil {
		log.Fatalf("Failed to parse git response payload: %v", err)
	}

	fmt.Println("--- Git Command Response from Daemon ---")
	if respPayload.Success {
		fmt.Println("Status: SUCCESS")
	} else {
		fmt.Println("Status: FAILED")
	}
	fmt.Printf("Output:\n%s\n", respPayload.Output)
	fmt.Println("----------------------------------------")
}

func sendListReposRequest(ctx context.Context, h host.Host, addrInfo peer.AddrInfo) {
	// TODO: Implement sending a LIST_REPOS_REQUEST and printing the response
}

func sendReadFileRequest(ctx context.Context, h host.Host, addrInfo peer.AddrInfo, repoAlias, filePath string) {
	// TODO: Implement sending a READ_FILE_REQUEST and printing the response
}
