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
	"regexp"
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

const linkedReposFile = "linked_repos.json"

func main() {
	// Command-line flags
	listenPort := flag.Int("port", 4001, "Port to listen on")
	repoFlag := flag.String("repo", "", "Alias and path to a git repo (e.g., my-project:/path/to/your/repo)")
	flag.Parse()

	// --- NEW: Load linked repos from file ---
	loadLinkedRepos()

	// If the file is empty and no flag is provided, we still need one repo.
	if len(linkedRepos) == 0 && *repoFlag == "" {
		log.Fatal("You must link at least one repository using the -repo flag on first run, or have a linked_repos.json file.")
	}
	// The flag can be used to add a repo on startup
	if *repoFlag != "" {
		parseRepoFlag(*repoFlag)
		saveLinkedRepos() // Save it immediately
	}

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
	case protocol.TypeListBranchesRequest:
		handleListBranches(stream, msg.Payload)
	case protocol.TypeLinkRepoRequest:
		handleLinkRepo(stream, msg.Payload)
	case protocol.TypeSwitchBranchRequest:
		handleSwitchBranch(stream, msg.Payload)
	case protocol.TypeGitStatusRequest:
		handleGitStatus(stream, msg.Payload)
	case protocol.TypeGitLogRequest:
		handleGitLog(stream, msg.Payload)
	case protocol.TypeGitDiffRequest:
		handleGitDiff(stream, msg.Payload)
	case protocol.TypeGitStashSaveRequest:
		handleGitStashSave(stream, msg.Payload)
	case protocol.TypeGitStashPopRequest:
		handleGitStashPop(stream, msg.Payload)
	case protocol.TypeGitResetRequest:
		handleGitReset(stream, msg.Payload)
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
	json.Unmarshal(rawPayload, &payload)
	log.Printf("Handling Git-aware Rename request in repo %s from %s to %s", payload.RepoPath, payload.OldPath, payload.NewPath)

	respPayload := protocol.RenameFileResponsePayload{}
	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Error = "unknown repository alias"
	} else {
		// --- THE FIX: Use `git mv` instead of `os.Rename` ---
		// The paths from the client are already relative to the repo root, which is what `git mv` wants.
		cmd := exec.Command("git", "mv", payload.OldPath, payload.NewPath)
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()

		if err != nil {
			respPayload.Success = false
			respPayload.Error = string(out)
		} else {
			respPayload.Success = true
		}
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeRenameFileResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleListBranches(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.ListBranchesRequestPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		log.Printf("Error unmarshalling list branches request: %v", err)
		return
	}
	log.Printf("Handling ListBranches request for repo %s", payload.RepoPath)

	respPayload := protocol.ListBranchesResponsePayload{}
	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Error = "unknown repository alias"
	} else {
		// git branch --format "%(refname:short)" is a clean way to get just branch names
		cmd := exec.Command("git", "branch", "--format", "%(refname:short)")
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			respPayload.Success = false
			respPayload.Error = string(out)
		} else {
			respPayload.Success = true
			// Split by newlines and filter out empty strings
			branchList := strings.Split(strings.TrimSpace(string(out)), "\n")
			var branches []string
			for _, branch := range branchList {
				if strings.TrimSpace(branch) != "" {
					branches = append(branches, strings.TrimSpace(branch))
				}
			}
			respPayload.Branches = branches
		}
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeListBranchesResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleLinkRepo(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.LinkRepoRequestPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		log.Printf("Error unmarshalling link repo request: %v", err)
		return
	}
	log.Printf("Handling LinkRepo request for alias %s", payload.Alias)

	respPayload := protocol.LinkRepoResponsePayload{}
	// On the daemon, the path is expected to be an absolute path
	// A real-world app might have more security here.
	absPath, err := filepath.Abs(payload.Path)
	if err != nil {
		respPayload.Success = false
		respPayload.Error = fmt.Sprintf("Invalid path: %v", err)
	} else {
		linkedRepos[payload.Alias] = absPath
		if err := saveLinkedRepos(); err != nil {
			respPayload.Success = false
			respPayload.Error = fmt.Sprintf("Failed to save repo list: %v", err)
		} else {
			respPayload.Success = true
		}
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeLinkRepoResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

// Helper function to find a specific stash's index
func findStashIndex(repoPath, stashMessage string) (string, bool) {
	// This command lists stashes with their index and message, e.g., "stash@{0}: p2p-auto-stash-for-master"
	cmd := exec.Command("git", "stash", "list")
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if strings.Contains(line, stashMessage) {
			// Use a regex to extract the stash index like "stash@{0}"
			re := regexp.MustCompile(`(stash@\{\d+\})`)
			match := re.FindStringSubmatch(line)
			if len(match) > 1 {
				return match[1], true // Return "stash@{0}"
			}
		}
	}
	return "", false
}

// Replace your entire `handleSwitchBranch` function with this new, smarter version.
func handleSwitchBranch(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.SwitchBranchRequestPayload
	json.Unmarshal(rawPayload, &payload)
	log.Printf("Handling SmartSwitch request for repo %s to branch %s", payload.RepoPath, payload.BranchName)

	respPayload := protocol.SwitchBranchResponsePayload{}
	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Output = "Error: unknown repository alias"
		// Send response and return
		payloadBytes, _ := json.Marshal(respPayload)
		response := &protocol.Message{Type: protocol.TypeSwitchBranchResponse, Payload: payloadBytes}
		protocol.WriteMessage(stream, response)
		return
	}

	// --- NEW SMART SWITCH LOGIC ---

	// 1. Get the current branch name on the daemon
	cmdCurrentBranch := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmdCurrentBranch.Dir = repoPath
	currentBranchBytes, err := cmdCurrentBranch.Output()
	if err != nil {
		// handle error...
		return
	}
	currentBranch := strings.TrimSpace(string(currentBranchBytes))

	if currentBranch == payload.BranchName {
		respPayload.Success = true
		respPayload.Output = fmt.Sprintf("Already on branch '%s'.", payload.BranchName)
	} else {
		// 2. Stash any current changes on the old branch
		stashMsg := fmt.Sprintf("p2p-auto-stash-for-%s", currentBranch)
		cmdStash := exec.Command("git", "stash", "save", "--include-untracked", stashMsg)
		cmdStash.Dir = repoPath
		cmdStash.Run() // We run this even if there are no changes to stash

		// 3. Checkout the new branch
		cmdCheckout := exec.Command("git", "checkout", payload.BranchName)
		cmdCheckout.Dir = repoPath
		out, err := cmdCheckout.CombinedOutput()
		if err != nil {
			respPayload.Success = false
			respPayload.Output = string(out)
		} else {
			// 4. Try to pop the stash for the NEW branch
			popStashMsg := fmt.Sprintf("p2p-auto-stash-for-%s", payload.BranchName)
			if index, found := findStashIndex(repoPath, popStashMsg); found {
				cmdPop := exec.Command("git", "stash", "pop", index)
				cmdPop.Dir = repoPath
				popOut, _ := cmdPop.CombinedOutput()
				respPayload.Output = fmt.Sprintf("Switched to branch '%s'.\nRestored previous work for this branch:\n%s", payload.BranchName, string(popOut))
			} else {
				respPayload.Output = fmt.Sprintf("Switched to branch '%s'. No previous work was stashed for this branch.", payload.BranchName)
			}
			respPayload.Success = true
		}
	}

	// Send final response
	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeSwitchBranchResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleGitStatus(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.GitStatusRequestPayload
	json.Unmarshal(rawPayload, &payload)
	log.Printf("Handling GitStatus request for repo %s", payload.RepoPath)

	respPayload := protocol.GitStatusResponsePayload{}
	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Output = "Error: unknown repository alias"
	} else {
		cmd := exec.Command("git", "status", "--porcelain")
		cmd.Dir = repoPath
		out, _ := cmd.CombinedOutput()
		respPayload.Success = true
		if len(out) == 0 {
			respPayload.Output = "Working tree is clean."
		} else {
			respPayload.Output = string(out)
		}
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeGitStatusResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleGitLog(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.GitLogRequestPayload
	json.Unmarshal(rawPayload, &payload)
	log.Printf("Handling GitLog request for repo %s", payload.RepoPath)

	respPayload := protocol.GitLogResponsePayload{}
	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Output = "Error: unknown repository alias"
	} else {
		cmd := exec.Command("git", "log", "--graph", "--pretty=format:'%Cred%h%Creset -%C(yellow)%d%Creset %s %Cgreen(%cr) %C(bold blue)<%an>%Creset'", "--abbrev-commit", "-n", "15")
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()

		respPayload.Success = (err == nil)
		respPayload.Output = string(out)
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeGitLogResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleGitDiff(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.GitDiffRequestPayload
	json.Unmarshal(rawPayload, &payload)
	log.Printf("Handling GitDiff request for repo %s, file %s", payload.RepoPath, payload.FilePath)

	respPayload := protocol.GitDiffResponsePayload{}
	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Output = "Error: unknown repository alias"
	} else {
		var cmd *exec.Cmd
		if payload.FilePath == "" {
			// Diff for the whole repo
			cmd = exec.Command("git", "diff", "--color")
		} else {
			// Diff for a specific file
			cmd = exec.Command("git", "diff", "--color", "--", payload.FilePath)
		}
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()

		respPayload.Success = (err == nil)
		if len(out) == 0 {
			respPayload.Output = "No differences found."
		} else {
			respPayload.Output = string(out)
		}
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeGitDiffResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleGitStashSave(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.GitStashSaveRequestPayload
	json.Unmarshal(rawPayload, &payload)
	log.Printf("Handling GitStashSave request for repo %s", payload.RepoPath)

	respPayload := protocol.GitStashSaveResponsePayload{}
	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Output = "Error: unknown repository alias"
	} else {
		// --- THE FIX: Add the --include-untracked flag ---
		cmd := exec.Command("git", "stash", "save", "--include-untracked", "p2p-remote-stash") // Optional message
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()

		respPayload.Success = (err == nil)
		respPayload.Output = string(out)
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeGitStashSaveResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleGitStashPop(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.GitStashPopRequestPayload
	json.Unmarshal(rawPayload, &payload)
	log.Printf("Handling GitStashPop request for repo %s", payload.RepoPath)

	respPayload := protocol.GitStashPopResponsePayload{}
	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Output = "Error: unknown repository alias"
	} else {
		// `git stash pop` applies the most recent stash and removes it from the list
		cmd := exec.Command("git", "stash", "pop")
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()

		respPayload.Success = (err == nil)
		respPayload.Output = string(out)
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeGitStashPopResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func handleGitReset(stream network.Stream, rawPayload json.RawMessage) {
	var payload protocol.GitResetRequestPayload
	json.Unmarshal(rawPayload, &payload)
	log.Printf("!!! DESTRUCTIVE ACTION: Handling GitReset request for repo %s", payload.RepoPath)

	respPayload := protocol.GitResetResponsePayload{}
	repoPath, ok := linkedRepos[payload.RepoPath]
	if !ok {
		respPayload.Success = false
		respPayload.Output = "Error: unknown repository alias"
	} else {
		cmd := exec.Command("git", "reset", "--hard", "HEAD")
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()
		respPayload.Success = (err == nil)
		respPayload.Output = string(out)
	}

	payloadBytes, _ := json.Marshal(respPayload)
	response := &protocol.Message{Type: protocol.TypeGitResetResponse, Payload: payloadBytes}
	protocol.WriteMessage(stream, response)
}

func getRepoAliases() []string {
	keys := make([]string, 0, len(linkedRepos))
	for k := range linkedRepos {
		keys = append(keys, k)
	}
	return keys
}

// NEW Function: Load repos from JSON file
func loadLinkedRepos() {
	linkedRepos = make(map[string]string)
	data, err := os.ReadFile(linkedReposFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("linked_repos.json not found, starting with empty repo list.")
			return
		}
		log.Fatalf("Failed to read linked repos file: %v", err)
	}
	if err := json.Unmarshal(data, &linkedRepos); err != nil {
		log.Fatalf("Failed to parse linked repos file: %v", err)
	}
	log.Printf("Loaded %d linked repos from %s", len(linkedRepos), linkedReposFile)
}

// NEW Function: Save repos to JSON file
func saveLinkedRepos() error {
	data, err := json.MarshalIndent(linkedRepos, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(linkedReposFile, data, 0644)
}
