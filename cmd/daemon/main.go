package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/skip2/go-qrcode"

	"github.com/hemantsingh443/p2p-git-remote/internal/git"
	p2p "github.com/hemantsingh443/p2p-git-remote/internal/p2p"
	"github.com/hemantsingh443/p2p-git-remote/internal/protocol"
	"github.com/hemantsingh443/p2p-git-remote/internal/store"
)

var trustStore *store.TrustStore
var linkedRepos map[string]string // Alias -> Path

func main() {
	// Command-line flags
	listenPort := flag.Int("port", 4001, "Port to listen on")
	repoFlag := flag.String("repo", "", "Alias and path to a git repo (e.g., my-project:/path/to/your/repo)")
	flag.Parse()

	if *repoFlag == "" {
		log.Fatal("You must link at least one repository using the -repo flag.")
	}
	parseRepoFlag(*repoFlag)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load or generate persistent identity
	privKey, err := p2p.LoadOrGeneratePrivateKey("daemon_identity.key")
	if err != nil {
		log.Fatalf("Failed to get private key: %v", err)
	}

	// Initialize TrustStore
	trustStore, err = store.NewTrustStore("trusted_peers.json")
	if err != nil {
		log.Fatalf("Failed to initialize trust store: %v", err)
	}

	// Create libp2p host
	h, err := p2p.CreateHost(ctx, privKey, *listenPort)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	// Start discovery
	go func() {
		if err := p2p.StartDiscovery(ctx, h); err != nil {
			log.Printf("Warning: Discovery failed: %v", err)
		}
	}()

	// Generate and display QR code
	addrInfo := peer.AddrInfo{
		ID:    h.ID(),
		Addrs: h.Addrs(),
	}
	addrs, err := peer.AddrInfoToP2pAddrs(&addrInfo)
	if err != nil {
		log.Fatalf("Failed to get p2p addresses: %v", err)
	}

	// We'll print the first public-facing address we find
	fmt.Println("====================================================================")
	fmt.Println("Scan the QR code with the mobile client to connect.")
	fmt.Println("Or copy the multiaddress below:")
	fmt.Println(addrs[0].String())
	fmt.Println("====================================================================")
	qrc, err := qrcode.New(addrs[0].String(), qrcode.Medium)
	if err != nil {
		log.Fatalf("Failed to generate QR code: %v", err)
	}
	fmt.Println(qrc.ToString(true))

	// Set a stream handler for our protocol
	h.SetStreamHandler(protocol.ProtocolID, handleStream)

	log.Println("Daemon is running. Waiting for connections...")
	select {} // Keep the daemon running
}

func parseRepoFlag(repoFlag string) {
	linkedRepos = make(map[string]string)
	parts := strings.Split(repoFlag, ":")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		log.Fatalf("Invalid repo flag format. Use 'alias:/path/to/repo'")
	}
	linkedRepos[parts[0]] = parts[1]
	log.Printf("Linked repository '%s' to path '%s'", parts[0], parts[1])
}

func handleStream(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	log.Printf("New stream from %s", remotePeer)
	defer stream.Close()

	if trustStore.IsTrusted(remotePeer) {
		log.Printf("Peer %s is already trusted. Listening for commands...", remotePeer)
		handleTrustedStream(stream)
	} else {
		log.Printf("Peer %s is not trusted. Initiating handshake...", remotePeer)
		handleHandshake(stream)
	}
}

func handleHandshake(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()

	// Wait for a handshake request
	msg, err := protocol.ReadMessage(stream)
	if err != nil {
		log.Printf("Failed to read handshake request from %s: %v", remotePeer, err)
		return
	}

	if msg.Type != "HANDSHAKE_REQUEST" {
		log.Printf("Expected HANDSHAKE_REQUEST from %s, but got %s", remotePeer, msg.Type)
		return
	}

	// Ask for user approval
	fmt.Printf("\n>>> New connection request from PeerID: %s\n", remotePeer)
	fmt.Print(">>> Approve this client? (y/n): ")

	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	approved := strings.TrimSpace(strings.ToLower(answer)) == "y"

	// Send response
	responsePayload := protocol.HandshakeResponsePayload{Approved: approved}
	payloadBytes, _ := json.Marshal(responsePayload)
	responseMsg := &protocol.Message{
		Type:    "HANDSHAKE_RESPONSE",
		Payload: payloadBytes,
	}

	if err := protocol.WriteMessage(stream, responseMsg); err != nil {
		log.Printf("Failed to send handshake response to %s: %v", remotePeer, err)
		return
	}

	if approved {
		if err := trustStore.AddTrustedPeer(remotePeer); err != nil {
			log.Printf("Failed to add peer %s to trust store: %v", remotePeer, err)
		} else {
			log.Printf("Peer %s approved and added to trust store.", remotePeer)
		}
	} else {
		log.Printf("Peer %s rejected.", remotePeer)
	}
}

func handleTrustedStream(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	log.Printf("Handling single command from trusted peer %s", remotePeer)

	// Read the single message the client is sending on this stream.
	msg, err := protocol.ReadMessage(stream)
	if err != nil {
		// This can happen if the client disconnects immediately.
		log.Printf("Failed to read command from %s: %v", remotePeer, err)
		return
	}

	// For now, we only handle git commit requests on trusted streams.
	// We can add a switch statement here later if we add more commands.
	if msg.Type == "GIT_COMMIT_REQUEST" {
		var payload protocol.GitCommitRequestPayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			log.Printf("Error unmarshalling git request: %v", err)
			return // Exit if payload is bad
		}

		repoPath, ok := linkedRepos[payload.RepoPath]
		if !ok {
			log.Printf("Received request for unknown repo alias: %s", payload.RepoPath)
			// Send an error response back to the client
			errorResponsePayload := protocol.GitCommitResponsePayload{
				Success: false,
				Output:  fmt.Sprintf("Error: Unknown repository alias '%s'. Daemon knows about: %v", payload.RepoPath, getRepoAliases()),
			}
			payloadBytes, _ := json.Marshal(errorResponsePayload)
			errorMsg := &protocol.Message{
				Type:    "GIT_COMMIT_RESPONSE",
				Payload: payloadBytes,
			}
			if err := protocol.WriteMessage(stream, errorMsg); err != nil {
				log.Printf("Failed to send error response to %s: %v", remotePeer, err)
			}
			return // Exit after sending error
		}

		log.Printf("Executing git commit on '%s' with message: '%s'", repoPath, payload.Message)
		// Assuming you've already changed "main" to "master" or your correct branch name
		output, err := git.CommitAndPush(repoPath, payload.Message, "origin", "master")

		// Send success/failure response
		responsePayload := protocol.GitCommitResponsePayload{
			Success: err == nil,
			Output:  output,
		}
		if err != nil && responsePayload.Output == "" {
			responsePayload.Output = err.Error()
		}

		payloadBytes, _ := json.Marshal(responsePayload)
		responseMsg := &protocol.Message{
			Type:    "GIT_COMMIT_RESPONSE",
			Payload: payloadBytes,
		}

		if err := protocol.WriteMessage(stream, responseMsg); err != nil {
			log.Printf("Failed to send git response to %s: %v", remotePeer, err)
		}
	} else {
		log.Printf("Received unexpected message type from trusted peer: %s", msg.Type)
	}

	// The function now ends. The `defer stream.Close()` in the parent `handleStream` function
	// will now correctly close this single-use stream.
}

func handleListRepos(stream network.Stream) {
	log.Println("Handling ListRepos request")
	payload := protocol.ListReposResponsePayload{Repos: getRepoAliases()}
	payloadBytes, _ := json.Marshal(payload)
	response := &protocol.Message{
		Type:    protocol.TypeListReposResponse,
		Payload: payloadBytes,
	}
	if err := protocol.WriteMessage(stream, response); err != nil {
		log.Printf("Failed to send repo list: %v", err)
	}
}

func handleReadFile(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.ReadFileRequestPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		// handle error
		return
	}
	log.Printf("Handling ReadFile request for %s in repo %s", payload.FilePath, payload.RepoPath)

	repoRoot, ok := linkedRepos[payload.RepoPath]
	if !ok {
		// handle unknown repo error
		return
	}

	// !!! SECURITY CRITICAL !!!
	// Prevent path traversal attacks (e.g., ../../etc/passwd)
	fullPath := filepath.Join(repoRoot, payload.FilePath)
	if !strings.HasPrefix(fullPath, filepath.Clean(repoRoot)+string(os.PathSeparator)) {
		// Send error response back
		return
	}

	content, err := os.ReadFile(fullPath)
	respPayload := protocol.ReadFileResponsePayload{}
	if err != nil {
		respPayload.Success = false
		respPayload.Error = err.Error()
	} else {
		respPayload.Success = true
		respPayload.Content = string(content)
	}

	// Send response
	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeReadFileResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleWriteFile(stream network.Stream, rawPayload json.RawMessage) {
	// Similar to ReadFile, with path validation and os.WriteFile
}

func getRepoAliases() []string {
	keys := make([]string, 0, len(linkedRepos))
	for k := range linkedRepos {
		keys = append(keys, k)
	}
	return keys
}
