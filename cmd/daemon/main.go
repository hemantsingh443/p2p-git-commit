package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
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

	// Convert the provided path to an absolute path
	absPath, err := filepath.Abs(parts[1])
	if err != nil {
		log.Fatalf("Could not get absolute path for repo: %v", err)
	}

	linkedRepos[parts[0]] = absPath
	log.Printf("Linked repository '%s' to absolute path '%s'", parts[0], absPath)
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

	// A trusted peer has connected. Read the one command they are sending.
	msg, err := protocol.ReadMessage(stream)
	if err != nil {
		if err.Error() != "EOF" { // It's normal for a client to close the stream (EOF)
			log.Printf("Failed to read command from trusted peer %s: %v", remotePeer, err)
		}
		return
	}

	log.Printf("Received command '%s' from trusted peer %s", msg.Type, remotePeer)

	// --- FIX: Use a switch to route to the correct handler ---
	switch msg.Type {
	case protocol.TypeGitCommitRequest:
		handleGitCommit(stream, msg.Payload)
	case protocol.TypeListReposRequest:
		handleListRepos(stream)
	case protocol.TypeReadFileRequest:
		handleReadFile(stream, msg.Payload)
	// case protocol.TypeWriteFileRequest:
	// 	handleWriteFile(stream, msg.Payload)
	// --- NEW CASES ---
	case protocol.TypeListFilesRequest:
		handleListFiles(stream, msg.Payload)
	case protocol.TypeCreateBranchRequest:
		handleCreateBranch(stream, msg.Payload)
	case protocol.TypeRenameFileRequest:
		handleRenameFile(stream, msg.Payload)
	case protocol.TypeWriteFileRequest:
		handleWriteFile(stream, msg.Payload)
	default:
		log.Printf("Received unknown message type from trusted peer: %s", msg.Type)
	}
}

// --- NEW: A dedicated handler for git commits ---
func handleGitCommit(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.GitCommitRequestPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		log.Printf("Error unmarshalling git request: %v", err)
		// Consider sending an error response back
		return
	}

	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		// Send an error response back to the client
		errOutput := fmt.Sprintf("Error: Unknown repository alias '%s'. Known aliases: %v", payload.RepoPath, getRepoAliases())
		errorResponsePayload := protocol.GitCommitResponsePayload{Success: false, Output: errOutput}
		payloadBytes, _ := json.Marshal(errorResponsePayload)
		errorMsg := &protocol.Message{Type: protocol.TypeGitCommitResponse, Payload: payloadBytes}
		protocol.WriteMessage(stream, errorMsg)
		return
	}

	log.Printf("Executing 'git commit & push' on '%s' for branch '%s'", repoPath, payload.Branch)
	output, err := git.CommitAndPush(repoPath, payload.Message, "origin", payload.Branch)

	responsePayload := protocol.GitCommitResponsePayload{Success: err == nil, Output: output}
	payloadBytes, _ := json.Marshal(responsePayload)
	responseMsg := &protocol.Message{Type: protocol.TypeGitCommitResponse, Payload: payloadBytes}

	if err := protocol.WriteMessage(stream, responseMsg); err != nil {
		log.Printf("Failed to send git response: %v", err)
	}
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

// --- Your existing stub, now implemented and used ---
func handleReadFile(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.ReadFileRequestPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		// You should send a proper error response here too
		log.Printf("Error unmarshalling read file request: %v", err)
		return
	}
	log.Printf("Handling ReadFile request for %s in repo %s", payload.FilePath, payload.RepoPath)

	respPayload := protocol.ReadFileResponsePayload{}
	repoRoot, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Error = fmt.Sprintf("Unknown repository alias: %s", payload.RepoPath)
	} else {
		// !!! SECURITY CRITICAL: Your path traversal prevention logic is good! Let's use it. !!!
		// This ensures the client can't request a file like `../../.ssh/id_rsa`
		fullPath := filepath.Join(repoRoot, payload.FilePath)
		cleanRepoRoot := filepath.Clean(repoRoot)

		if !strings.HasPrefix(fullPath, cleanRepoRoot) {
			respPayload.Success = false
			respPayload.Error = "Access denied: path is outside of repository root"
		} else {
			content, err := os.ReadFile(fullPath)
			if err != nil {
				respPayload.Success = false
				respPayload.Error = err.Error()
			} else {
				respPayload.Success = true
				respPayload.Content = string(content)
			}
		}
	}

	// Send response
	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeReadFileResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleWriteFile(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.WriteFileRequestPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		// handle error properly
		return
	}
	log.Printf("Handling WriteFile request for %s in repo %s", payload.FilePath, payload.RepoPath)

	respPayload := protocol.WriteFileResponsePayload{}
	repoRoot, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Error = "unknown repository alias"
	} else {
		// !!! SECURITY CRITICAL: Path validation is essential here too!
		fullPath := filepath.Join(repoRoot, payload.FilePath)
		cleanRepoRoot := filepath.Clean(repoRoot)

		if !strings.HasPrefix(fullPath, cleanRepoRoot) {
			respPayload.Success = false
			respPayload.Error = "Access denied: path is outside of repository root"
		} else {
			// Write the file, creating it if it doesn't exist. 0644 is standard file permissions.
			err := os.WriteFile(fullPath, []byte(payload.Content), 0644)
			if err != nil {
				respPayload.Success = false
				respPayload.Error = err.Error()
			} else {
				respPayload.Success = true
			}
		}
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeWriteFileResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleListFiles(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.ListFilesRequestPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		// handle error properly
		return
	}
	log.Printf("Handling ListFiles request for repo %s", payload.RepoPath)

	respPayload := protocol.ListFilesResponsePayload{}
	repoRoot, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Error = "unknown repository alias"
	} else {
		// Add debugging to see what path we're walking
		log.Printf("Walking directory: %s", repoRoot)

		var files []string
		err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				log.Printf("Error walking path %s: %v", path, err)
				return err
			}

			// Debug: log what we're finding
			log.Printf("Found: %s (isDir: %t)", path, d.IsDir())

			// Skip the .git directory
			if d.IsDir() && d.Name() == ".git" {
				log.Printf("Skipping .git directory: %s", path)
				return filepath.SkipDir
			}

			// Don't skip the root directory itself, just skip .git
			if !d.IsDir() {
				// Make the path relative to the repo root
				relativePath, err := filepath.Rel(repoRoot, path)
				if err != nil {
					log.Printf("Error making path relative: %v", err)
					return err
				}
				log.Printf("Adding file: %s", relativePath)
				files = append(files, relativePath)
			}
			return nil
		})

		if err != nil {
			respPayload.Success = false
			respPayload.Error = err.Error()
		} else {
			respPayload.Success = true
			respPayload.Files = files
		}
	}

	// --- ADD THIS DEBUG LINE ---
	log.Printf("Daemon found %d files to send: %v", len(respPayload.Files), respPayload.Files)

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeListFilesResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleCreateBranch(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.CreateBranchRequestPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		log.Printf("Error unmarshalling create branch request: %v", err)
		return
	}
	log.Printf("Handling CreateBranch request for repo %s, branch %s", payload.RepoPath, payload.NewBranchName)

	respPayload := protocol.CreateBranchResponsePayload{}
	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Output = fmt.Sprintf("Unknown repository alias: %s", payload.RepoPath)
	} else {
		// Here we just create the branch, we don't switch to it on the daemon.
		cmd := exec.Command("git", "branch", payload.NewBranchName)
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			respPayload.Success = false
			respPayload.Output = string(out)
		} else {
			respPayload.Success = true
			respPayload.Output = fmt.Sprintf("Branch '%s' created.", payload.NewBranchName)
		}
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeCreateBranchResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleRenameFile(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.RenameFileRequestPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		log.Printf("Error unmarshalling rename file request: %v", err)
		return
	}
	log.Printf("Handling RenameFile request in repo %s from %s to %s", payload.RepoPath, payload.OldPath, payload.NewPath)

	respPayload := protocol.RenameFileResponsePayload{}
	repoRoot, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Error = fmt.Sprintf("Unknown repository alias: %s", payload.RepoPath)
	} else {
		// !!! SECURITY CRITICAL: Validate both paths are inside the repo !!!
		oldFullPath := filepath.Join(repoRoot, payload.OldPath)
		newFullPath := filepath.Join(repoRoot, payload.NewPath)
		cleanRepoRoot := filepath.Clean(repoRoot)

		if !strings.HasPrefix(oldFullPath, cleanRepoRoot) || !strings.HasPrefix(newFullPath, cleanRepoRoot) {
			respPayload.Success = false
			respPayload.Error = "Access denied: path is outside of repository root"
		} else {
			err := os.Rename(oldFullPath, newFullPath)
			if err != nil {
				respPayload.Success = false
				respPayload.Error = err.Error()
			} else {
				respPayload.Success = true
			}
		}
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeRenameFileResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func getRepoAliases() []string {
	keys := make([]string, 0, len(linkedRepos))
	for k := range linkedRepos {
		keys = append(keys, k)
	}
	return keys
}
