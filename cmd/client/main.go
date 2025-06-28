package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/c-bata/go-prompt"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	p2p "github.com/hemantsingh443/p2p-git-remote/internal/p2p"
	"github.com/hemantsingh443/p2p-git-remote/internal/protocol"
	"github.com/hemantsingh443/p2p-git-remote/internal/store"
)

// clientState holds the application's current state.
type clientState struct {
	p2pHost       host.Host
	daemonInfo    peer.AddrInfo
	trustStore    *store.TrustStore
	currentRepo   string
	currentBranch string
	livePrefix    string
}

func main() {
	// Only one flag needed now: the daemon's address
	if len(os.Args) < 2 {
		log.Fatal("Usage: ./client <daemon-multiaddress>")
	}
	daemonAddr := os.Args[1]

	ctx := context.Background()

	// --- Setup P2P Host and Trust ---
	privKey, err := p2p.LoadOrGeneratePrivateKey("client_identity.key")
	if err != nil {
		log.Fatalf("Failed to get private key: %v", err)
	}

	h, err := p2p.CreateHost(ctx, privKey, 0)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	go func() {
		if err := p2p.StartDiscovery(ctx, h); err != nil {
			log.Printf("Warning: Discovery failed: %v", err)
		}
	}()

	maddr, err := multiaddr.NewMultiaddr(daemonAddr)
	if err != nil {
		log.Fatalf("Invalid multiaddress: %v", err)
	}

	addrInfo, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		log.Fatalf("Failed to parse peer address info: %v", err)
	}

	trustStore, err := store.NewTrustStore("client_trusted_daemon.json")
	if err != nil {
		log.Fatalf("Failed to open client trust store: %v", err)
	}

	// --- Connect and Handshake if needed ---
	if err := h.Connect(ctx, *addrInfo); err != nil {
		log.Fatalf("Failed to connect to daemon: %v", err)
	}
	fmt.Println("Connection to daemon established.")

	if !trustStore.IsTrusted(addrInfo.ID) {
		performHandshake(ctx, h, *addrInfo, trustStore)
	} else {
		fmt.Println("Daemon is already trusted.")
	}

	// --- Initialize State and Start REPL ---
	state := &clientState{
		p2pHost:       h,
		daemonInfo:    *addrInfo,
		trustStore:    trustStore,
		currentRepo:   "",
		currentBranch: "master", // Default
		livePrefix:    "p2p-git(no repo)> ",
	}

	p := prompt.New(
		executor(state),
		completer,
		prompt.OptionPrefix(state.livePrefix),
		prompt.OptionTitle("p2p-git-remote"),
		prompt.OptionLivePrefix(state.changeLivePrefix),
	)
	p.Run()
}

// executor is the heart of the REPL. It parses and executes commands.
func executor(state *clientState) func(s string) {
	return func(s string) {
		s = strings.TrimSpace(s)
		parts := strings.Fields(s)
		if len(parts) == 0 {
			return
		}

		command := parts[0]
		args := parts[1:]

		// --- FIX: Only create a stream for commands that need it ---
		needsStream := true
		switch command {
		case "exit", "quit", "help", "use":
			needsStream = false
		}

		var stream network.Stream
		var err error
		if needsStream {
			ctx := context.Background()
			stream, err = state.p2pHost.NewStream(ctx, state.daemonInfo.ID, protocol.ProtocolID)
			if err != nil {
				fmt.Printf("Error: could not create stream: %v\n", err)
				return
			}
			defer stream.Close()
		}

		// --- Command routing ---
		switch command {
		case "exit", "quit":
			fmt.Println("Bye!")
			os.Exit(0)
		case "help":
			printHelp()
		case "use":
			if len(args) < 1 {
				fmt.Println("Usage: use <repo-alias>")
				return
			}
			state.currentRepo = args[0]
			fmt.Printf("Switched to repo: %s\n", state.currentRepo)

		// --- Commands that need a stream ---
		case "ls-repos":
			handleListRepos(stream)
		case "ls":
			if state.currentRepo == "" {
				fmt.Println("No repository selected. Use 'use <repo-alias>' first.")
				return
			}
			handleListFiles(stream, state.currentRepo)
		case "branch":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			if len(args) < 1 {
				fmt.Println("Usage: branch <new-branch-name>")
				return
			}
			handleCreateBranch(stream, state.currentRepo, args[0])
			state.currentBranch = args[0]
			fmt.Printf("Client context switched to new branch: %s\n", state.currentBranch)
		case "commit":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			if len(args) < 1 {
				fmt.Println("Usage: commit <message>")
				return
			}
			msg := strings.Join(args, " ")
			handleCommit(stream, state.currentRepo, state.currentBranch, msg)
		case "rename":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			if len(args) < 2 {
				fmt.Println("Usage: rename <old-path> <new-path>")
				return
			}
			handleRenameFile(stream, state.currentRepo, args[0], args[1])

		// --- Commands that need context but not a direct stream ---
		case "cat":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			if len(args) < 1 {
				fmt.Println("Usage: cat <file-path>")
				return
			}
			content, err := readFileRemote(context.Background(), state, args[0])
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			} else {
				fmt.Println(string(content))
			}
		case "edit":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			if len(args) < 1 {
				fmt.Println("Usage: edit <file-path>")
				return
			}
			handleEditFile(context.Background(), state, args[0])
		default:
			fmt.Println("Unknown command. Type 'help' for a list of commands.")
		}
	}
}

// --- All the helper functions for executor go here ---

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

func handleListRepos(stream network.Stream) {
	req := &protocol.Message{Type: protocol.TypeListReposRequest}
	protocol.WriteMessage(stream, req)
	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		fmt.Printf("Error reading response: %v\n", err)
		return
	}

	var payload protocol.ListReposResponsePayload
	json.Unmarshal(resp.Payload, &payload)
	fmt.Println("--- Available Repositories ---")
	for _, repo := range payload.Repos {
		fmt.Printf("- %s\n", repo)
	}
	fmt.Println("------------------------------")
}

func handleListFiles(stream network.Stream, repoAlias string) {
	// 1. Create and send the request
	reqPayload := protocol.ListFilesRequestPayload{RepoPath: repoAlias}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeListFilesRequest, Payload: payloadBytes}
	if err := protocol.WriteMessage(stream, req); err != nil {
		fmt.Printf("Error sending 'ls' request: %v\n", err)
		return
	}

	// 2. Read the response from the daemon
	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		fmt.Printf("Error reading 'ls' response: %v\n", err)
		return
	}

	// 3. Unmarshal the response payload
	var respPayload protocol.ListFilesResponsePayload
	if err := json.Unmarshal(resp.Payload, &respPayload); err != nil {
		fmt.Printf("Error parsing 'ls' response payload: %v\n", err)
		return
	}

	// 4. Check for an error message from the daemon
	if !respPayload.Success {
		fmt.Printf("Error from daemon: %s\n", respPayload.Error)
		return
	}

	// 5. THIS IS THE CRITICAL PART: Print the files
	for _, file := range respPayload.Files {
		fmt.Println(file)
	}
}

func handleCreateBranch(stream network.Stream, repoAlias, newBranch string) {
	reqPayload := protocol.CreateBranchRequestPayload{RepoPath: repoAlias, NewBranchName: newBranch}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeCreateBranchRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, _ := protocol.ReadMessage(stream)
	var respPayload protocol.CreateBranchResponsePayload
	json.Unmarshal(resp.Payload, &respPayload)

	if !respPayload.Success {
		fmt.Printf("Error creating branch: %s\n", respPayload.Output)
	} else {
		fmt.Println("Branch created successfully on daemon.")
	}
}

func handleRenameFile(stream network.Stream, repoAlias, oldPath, newPath string) {
	reqPayload := protocol.RenameFileRequestPayload{
		RepoPath: repoAlias,
		OldPath:  oldPath,
		NewPath:  newPath,
	}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeRenameFileRequest, Payload: payloadBytes}

	if err := protocol.WriteMessage(stream, req); err != nil {
		fmt.Printf("Error sending rename request: %v\n", err)
		return
	}

	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		fmt.Printf("Error reading rename response: %v\n", err)
		return
	}

	var respPayload protocol.RenameFileResponsePayload
	json.Unmarshal(resp.Payload, &respPayload)

	if !respPayload.Success {
		fmt.Printf("Error from daemon: %s\n", respPayload.Error)
	} else {
		fmt.Printf("Successfully renamed '%s' to '%s' on the daemon.\n", oldPath, newPath)
	}
}

func readFileRemote(ctx context.Context, state *clientState, filePath string) ([]byte, error) {
	stream, err := state.p2pHost.NewStream(ctx, state.daemonInfo.ID, protocol.ProtocolID)
	if err != nil {
		return nil, fmt.Errorf("could not create stream: %v", err)
	}
	defer stream.Close()

	reqPayload := protocol.ReadFileRequestPayload{
		RepoPath: state.currentRepo,
		FilePath: filePath,
	}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeReadFileRequest, Payload: payloadBytes}

	if err := protocol.WriteMessage(stream, req); err != nil {
		return nil, fmt.Errorf("failed to send read request: %v", err)
	}

	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		return nil, fmt.Errorf("failed to read read response: %v", err)
	}

	var respPayload protocol.ReadFileResponsePayload
	if err := json.Unmarshal(resp.Payload, &respPayload); err != nil {
		return nil, fmt.Errorf("failed to parse read response payload: %v", err)
	}

	if !respPayload.Success {
		return nil, fmt.Errorf("daemon error: %s", respPayload.Error)
	}

	return []byte(respPayload.Content), nil
}

func writeFileRemote(ctx context.Context, state *clientState, filePath, content string) error {
	stream, err := state.p2pHost.NewStream(ctx, state.daemonInfo.ID, protocol.ProtocolID)
	if err != nil {
		return fmt.Errorf("could not create stream: %v", err)
	}
	defer stream.Close()

	reqPayload := protocol.WriteFileRequestPayload{
		RepoPath: state.currentRepo,
		FilePath: filePath,
		Content:  content,
	}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeWriteFileRequest, Payload: payloadBytes}

	if err := protocol.WriteMessage(stream, req); err != nil {
		return fmt.Errorf("failed to send write request: %v", err)
	}

	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		return fmt.Errorf("failed to read write response: %v", err)
	}

	var respPayload protocol.WriteFileResponsePayload
	if err := json.Unmarshal(resp.Payload, &respPayload); err != nil {
		return fmt.Errorf("error parsing response: %v", err)
	}

	if !respPayload.Success {
		return fmt.Errorf("daemon error: %s", respPayload.Error)
	}

	fmt.Printf("Successfully wrote changes to %s on the daemon.\n", filePath)
	return nil
}

// The clever `edit` implementation
func handleEditFile(ctx context.Context, state *clientState, filePath string) {
	fmt.Printf("Downloading %s for editing...\n", filePath)
	content, err := readFileRemote(ctx, state, filePath)
	if err != nil {
		fmt.Printf("Could not read remote file: %v\n", err)
		return
	}

	// Create a temporary file
	tmpfile, err := ioutil.TempFile("", "p2p-edit-*.tmp")
	if err != nil {
		fmt.Printf("Could not create temp file: %v\n", err)
		return
	}
	defer os.Remove(tmpfile.Name()) // Clean up

	tmpfile.Write(content)
	tmpfile.Close()

	// Open the default system editor
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim" // A sensible default
	}

	fmt.Printf("Opening %s in %s... (save and close editor to upload changes)\n", filePath, editor)
	cmd := exec.Command(editor, tmpfile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Editor command failed: %v\n", err)
		return
	}

	// Read the (potentially) modified content back
	newContent, err := ioutil.ReadFile(tmpfile.Name())
	if err != nil {
		fmt.Printf("Could not read modified file: %v\n", err)
		return
	}

	// Upload the new content
	fmt.Println("Uploading changes...")
	if err := writeFileRemote(ctx, state, filePath, string(newContent)); err != nil {
		fmt.Printf("Failed to upload changes: %v\n", err)
	}
}

func handleCommit(stream network.Stream, repo, branch, msg string) {
	gitPayload := protocol.GitCommitRequestPayload{RepoPath: repo, Message: msg, Branch: branch}
	payloadBytes, _ := json.Marshal(gitPayload)
	gitReq := &protocol.Message{Type: protocol.TypeGitCommitRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, gitReq)

	gitResponse, _ := protocol.ReadMessage(stream)
	var respPayload protocol.GitCommitResponsePayload
	json.Unmarshal(gitResponse.Payload, &respPayload)

	fmt.Println("--- Git Command Response from Daemon ---")
	if respPayload.Success {
		fmt.Println("Status: SUCCESS")
	} else {
		fmt.Println("Status: FAILED")
	}
	fmt.Printf("Output:\n%s\n", respPayload.Output)
	fmt.Println("----------------------------------------")
}

func printHelp() {
	fmt.Println("Available commands:")
	fmt.Println("  help          Show this help message")
	fmt.Println("  ls-repos      List available repositories on the daemon")
	fmt.Println("  use <repo>    Switch context to a repository")
	fmt.Println("  ls            List files in the current repository")
	fmt.Println("  cat <file>    Display content of a remote file")
	fmt.Println("  edit <file>   Download, edit, and upload a file")
	fmt.Println("  rename <old> <new> Rename a remote file")
	fmt.Println("  branch <name> Create a new branch on the daemon")
	fmt.Println("  commit <msg>  Commit all changes in the repo and push to the current branch")
	fmt.Println("  exit, quit    Close the application")
}

func (s *clientState) changeLivePrefix() (string, bool) {
	s.livePrefix = fmt.Sprintf("p2p-git(%s @ %s)> ", s.currentRepo, s.currentBranch)
	if s.currentRepo == "" {
		s.livePrefix = "p2p-git(no repo)> "
	}
	return s.livePrefix, true
}

func completer(d prompt.Document) []prompt.Suggest {
	// Simple completer
	s := []prompt.Suggest{
		{Text: "help", Description: "Show help"},
		{Text: "ls-repos", Description: "List available repositories"},
		{Text: "use", Description: "Switch to a repository context. Usage: use <repo-alias>"},
		{Text: "ls", Description: "List files in the current repository"},
		{Text: "cat", Description: "Display the content of a remote file"},
		{Text: "edit", Description: "Edit a remote file locally"},
		{Text: "rename", Description: "Rename a file. Usage: rename <old> <new>"},
		{Text: "branch", Description: "Create a new git branch"},
		{Text: "commit", Description: "Commit all changes with a message"},
		{Text: "exit", Description: "Exit the shell"},
	}
	return prompt.FilterHasPrefix(s, d.GetWordBeforeCursor(), true)
}
