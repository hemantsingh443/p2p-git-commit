package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/c-bata/go-prompt"
	"github.com/fatih/color"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	tea "github.com/charmbracelet/bubbletea"
	p2p "github.com/hemantsingh443/p2p-git-remote/internal/p2p"
	"github.com/hemantsingh443/p2p-git-remote/internal/protocol"
	"github.com/hemantsingh443/p2p-git-remote/internal/store"
	"github.com/hemantsingh443/p2p-git-remote/internal/tui"
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

type ClientConfig map[string]string

type ConfigManager struct {
	Path   string
	Config ClientConfig
}

func NewConfigManager() (*ConfigManager, error) {
	home, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("could not find home directory: %w", err)
	}
	configPath := filepath.Join(home.HomeDir, ".p2p-git", "config.json")

	// Ensure the directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return nil, fmt.Errorf("could not create config directory: %w", err)
	}

	cm := &ConfigManager{Path: configPath, Config: make(ClientConfig)}
	return cm, cm.Load()
}

func (cm *ConfigManager) Load() error {
	data, err := os.ReadFile(cm.Path)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist, which is fine. Start with an empty config.
			return nil
		}
		return fmt.Errorf("could not read config file: %w", err)
	}
	return json.Unmarshal(data, &cm.Config)
}

func (cm *ConfigManager) Save() error {
	data, err := json.MarshalIndent(cm.Config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cm.Path, data, 0644)
}

func (cm *ConfigManager) AddDaemon(name, addr string) {
	cm.Config[name] = addr
}

func main() {
	if len(os.Args) < 2 {
		// Updated usage message
		fmt.Println("Usage: ./client <daemon-name> [tui] | link <new-daemon-name>")
		os.Exit(1)
	}

	command := os.Args[1]

	configManager, err := NewConfigManager()
	if err != nil {
		log.Fatal(err)
	}

	// --- MODE 1: Linking a new daemon ---
	if command == "link" {
		if len(os.Args) < 3 {
			fmt.Println("Usage: ./client link <new-daemon-name>")
			os.Exit(1)
		}
		daemonName := os.Args[2]

		// Prompt the user for the multiaddress
		fmt.Printf("Please scan or paste the multiaddress for '%s':\n> ", daemonName)
		reader := bufio.NewReader(os.Stdin)
		daemonAddr, _ := reader.ReadString('\n')
		daemonAddr = strings.TrimSpace(daemonAddr)

		// Validate the address before saving
		if _, err := multiaddr.NewMultiaddr(daemonAddr); err != nil {
			color.Red("Error: Invalid multiaddress provided. Aborting.")
			os.Exit(1)
		}

		configManager.AddDaemon(daemonName, daemonAddr)
		if err := configManager.Save(); err != nil {
			color.Red("Failed to save config: %v", err)
			os.Exit(1)
		}
		color.Green("Successfully linked '%s'. You can now connect using './client %s'", daemonName, daemonName)
		return // Exit after linking
	}

	// --- NEW LOGIC: Check for 'tui' command ---
	isTuiMode := false
	daemonName := command
	if len(os.Args) > 2 && os.Args[2] == "tui" {
		isTuiMode = true
	}

	// --- MODE 2: Connecting to an existing daemon ---
	daemonAddr, ok := configManager.Config[daemonName]
	if !ok {
		color.Red("Error: Daemon name '%s' not found in your config file.", daemonName)
		fmt.Println("Use './client link <name>' to add it.")
		os.Exit(1)
	}

	ctx := context.Background()

	// Load or generate persistent identity
	privKey, err := p2p.LoadOrGeneratePrivateKey("client_identity.key")
	if err != nil {
		log.Fatalf("Failed to get private key: %v", err)
	}

	// Create libp2p host
	h, err := p2p.CreateHost(ctx, privKey, 0) // Port 0 means random port
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	// Parse the daemon's multiaddress
	addrInfo, err := peer.AddrInfoFromString(daemonAddr)
	if err != nil {
		log.Fatalf("Failed to parse daemon address: %v", err)
	}

	// Connect to the daemon
	if err := h.Connect(ctx, *addrInfo); err != nil {
		log.Fatalf("Failed to connect to daemon: %v", err)
	}

	// Initialize TrustStore
	trustStore, err := store.NewTrustStore("trusted_daemons.json")
	if err != nil {
		log.Fatalf("Failed to initialize trust store: %v", err)
	}

	if !trustStore.IsTrusted(addrInfo.ID) {
		performHandshake(ctx, h, *addrInfo, trustStore)
	} else {
		fmt.Println("Daemon is already trusted.")
	}

	// --- The final part of main is now a switch ---
	// We pass the core client state to both modes
	appState := &tui.AppState{
		P2pHost:       h,
		DaemonInfo:    *addrInfo,
		CurrentRepo:   "my-project", // You might want to make this selectable
		CurrentBranch: "master",
	}

	if isTuiMode {
		// --- LAUNCH TUI MODE ---
		p := tea.NewProgram(tui.NewModel(appState), tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			log.Fatalf("Error running TUI: %v", err)
		}
	} else {
		// --- LAUNCH REPL MODE (your existing code) ---
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
		case "exit", "quit", "help":
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
			handleUseRepo(stream, state, args[0])

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
			handleCreateBranch(stream, state, args[0])
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
		case "branches":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			handleListBranches(stream, state.currentRepo)
		case "switch":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			if len(args) < 1 {
				fmt.Println("Usage: switch <branch-name>")
				return
			}
			handleSwitchBranch(stream, state, args[0])
		case "link":
			if len(args) < 2 {
				fmt.Println("Usage: link <alias> <absolute-path-on-daemon>")
				return
			}
			handleLinkRepo(stream, args[0], args[1])
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
		case "status":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			handleGitStatus(stream, state.currentRepo)
		case "log":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			handleGitLog(stream, state.currentRepo)
		case "diff":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			filePath := ""
			if len(args) > 0 {
				filePath = args[0]
			}
			handleGitDiff(stream, state.currentRepo, filePath)
		case "stash":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			handleGitStashSave(stream, state.currentRepo)
		case "stash-pop":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			handleGitStashPop(stream, state.currentRepo)
		case "reset":
			if state.currentRepo == "" {
				fmt.Println("No repository selected.")
				return
			}
			handleGitReset(stream, state.currentRepo)

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
		color.Red("Error reading response: %v", err)
		return
	}

	var payload protocol.ListReposResponsePayload
	json.Unmarshal(resp.Payload, &payload)
	color.Cyan("--- Available Repositories ---")
	for _, repo := range payload.Repos {
		color.Yellow("- %s", repo)
	}
	color.Cyan("------------------------------")
}

func handleListFiles(stream network.Stream, repoAlias string) {
	// 1. Create and send the request
	reqPayload := protocol.ListFilesRequestPayload{RepoPath: repoAlias}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeListFilesRequest, Payload: payloadBytes}
	if err := protocol.WriteMessage(stream, req); err != nil {
		color.Red("Error sending 'ls' request: %v", err)
		return
	}

	// 2. Read the response from the daemon
	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		color.Red("Error reading 'ls' response: %v", err)
		return
	}

	// 3. Unmarshal the response payload
	var respPayload protocol.ListFilesResponsePayload
	if err := json.Unmarshal(resp.Payload, &respPayload); err != nil {
		color.Red("Error parsing 'ls' response payload: %v", err)
		return
	}

	// 4. Check for an error message from the daemon
	if !respPayload.Success {
		color.Red("Error from daemon: %s", respPayload.Error)
		return
	}

	// 5. THIS IS THE CRITICAL PART: Print the files
	color.Cyan("--- Files in Repository ---")
	for _, file := range respPayload.Files {
		color.White(file)
	}
	color.Cyan("---------------------------")
}

func handleCreateBranch(stream network.Stream, state *clientState, newBranch string) {
	reqPayload := protocol.CreateBranchRequestPayload{RepoPath: state.currentRepo, NewBranchName: newBranch}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeCreateBranchRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		fmt.Printf("Error reading branch response: %v\n", err)
		return
	}

	var respPayload protocol.CreateBranchResponsePayload
	if err := json.Unmarshal(resp.Payload, &respPayload); err != nil {
		fmt.Printf("Error parsing branch response payload: %v\n", err)
		return
	}

	if !respPayload.Success {
		fmt.Printf("Error creating branch: %s\n", respPayload.Output)
	} else {
		fmt.Println("Branch created successfully on daemon.")
		// --- IMPORTANT: Only change client state on success! ---
		state.currentBranch = newBranch
		fmt.Printf("Client context switched to new branch: %s\n", state.currentBranch)
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

func handleCommit(stream network.Stream, repoAlias, branch, message string) {
	reqPayload := protocol.GitCommitRequestPayload{
		RepoPath: repoAlias,
		Message:  message,
		Branch:   branch,
	}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeGitCommitRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		color.Red("Error reading commit response: %v", err)
		return
	}

	var respPayload protocol.GitCommitResponsePayload
	if err := json.Unmarshal(resp.Payload, &respPayload); err != nil {
		color.Red("Error parsing commit response payload: %v", err)
		return
	}

	if !respPayload.Success {
		color.Red("Commit failed:\n%s", respPayload.Output)
	} else {
		color.Green("Commit successful!")
		color.Cyan("Output:")
		fmt.Println(respPayload.Output)
	}
}

func handleListBranches(stream network.Stream, repoAlias string) {
	reqPayload := protocol.ListBranchesRequestPayload{RepoPath: repoAlias}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeListBranchesRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		fmt.Printf("Error reading branches response: %v\n", err)
		return
	}

	var respPayload protocol.ListBranchesResponsePayload
	if err := json.Unmarshal(resp.Payload, &respPayload); err != nil {
		fmt.Printf("Error parsing branches response payload: %v\n", err)
		return
	}

	if !respPayload.Success {
		fmt.Printf("Error from daemon: %s\n", respPayload.Error)
		return
	}

	fmt.Println("--- Available Branches ---")
	for _, branch := range respPayload.Branches {
		fmt.Println(branch)
	}
	fmt.Println("------------------------------")
}

func handleLinkRepo(stream network.Stream, alias, path string) {
	reqPayload := protocol.LinkRepoRequestPayload{Alias: alias, Path: path}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeLinkRepoRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		fmt.Printf("Error reading link response: %v\n", err)
		return
	}

	var respPayload protocol.LinkRepoResponsePayload
	if err := json.Unmarshal(resp.Payload, &respPayload); err != nil {
		fmt.Printf("Error parsing link response payload: %v\n", err)
		return
	}

	if !respPayload.Success {
		fmt.Printf("Failed to link repo: %s\n", respPayload.Error)
	} else {
		fmt.Printf("Successfully linked '%s' on daemon.\n", alias)
	}
}

func handleSwitchBranch(stream network.Stream, state *clientState, branchName string) {
	reqPayload := protocol.SwitchBranchRequestPayload{
		RepoPath:   state.currentRepo,
		BranchName: branchName,
	}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeSwitchBranchRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		fmt.Printf("Error reading switch response: %v\n", err)
		return
	}

	var respPayload protocol.SwitchBranchResponsePayload
	if err := json.Unmarshal(resp.Payload, &respPayload); err != nil {
		fmt.Printf("Error parsing switch response payload: %v\n", err)
		return
	}

	if !respPayload.Success {
		color.Red("Error from daemon:\n%s", respPayload.Output)
	} else {
		color.Green("Daemon switched to branch '%s'.", branchName)
		state.currentBranch = branchName
	}
}

func handleGitStatus(stream network.Stream, repoAlias string) {
	reqPayload := protocol.GitStatusRequestPayload{RepoPath: repoAlias}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeGitStatusRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)
	resp, _ := protocol.ReadMessage(stream)
	var respPayload protocol.GitStatusResponsePayload
	json.Unmarshal(resp.Payload, &respPayload)

	color.Cyan("--- Git Status ---")
	if !respPayload.Success {
		color.Red("Error from daemon: %s", respPayload.Output)
	} else {
		fmt.Print(respPayload.Output)
	}
	color.Cyan("------------------")
}

func handleGitLog(stream network.Stream, repoAlias string) {
	reqPayload := protocol.GitLogRequestPayload{RepoPath: repoAlias}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeGitLogRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, _ := protocol.ReadMessage(stream)
	var respPayload protocol.GitLogResponsePayload
	json.Unmarshal(resp.Payload, &respPayload)

	if !respPayload.Success {
		color.Red("Error from daemon: %s", respPayload.Output)
	} else {
		color.Cyan("--- Git Log ---")
		fmt.Println(respPayload.Output)
		color.Cyan("---------------")
	}
}

func handleGitDiff(stream network.Stream, repoAlias, filePath string) {
	reqPayload := protocol.GitDiffRequestPayload{RepoPath: repoAlias, FilePath: filePath}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeGitDiffRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, _ := protocol.ReadMessage(stream)
	var respPayload protocol.GitDiffResponsePayload
	json.Unmarshal(resp.Payload, &respPayload)

	if !respPayload.Success {
		color.Red("Error from daemon: %s", respPayload.Output)
	} else {
		color.Cyan("--- Git Diff ---")
		fmt.Println(respPayload.Output)
		color.Cyan("----------------")
	}
}

func handleGitStashSave(stream network.Stream, repoAlias string) {
	reqPayload := protocol.GitStashSaveRequestPayload{RepoPath: repoAlias}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeGitStashSaveRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, _ := protocol.ReadMessage(stream)
	var respPayload protocol.GitStashSaveResponsePayload
	json.Unmarshal(resp.Payload, &respPayload)

	if !respPayload.Success {
		color.Red("Error stashing changes:\n%s", respPayload.Output)
	} else {
		color.Green("--- Stash Result ---")
		fmt.Print(respPayload.Output)
		color.Green("--------------------")
	}
}

func handleGitStashPop(stream network.Stream, repoAlias string) {
	reqPayload := protocol.GitStashPopRequestPayload{RepoPath: repoAlias}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeGitStashPopRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, _ := protocol.ReadMessage(stream)
	var respPayload protocol.GitStashPopResponsePayload
	json.Unmarshal(resp.Payload, &respPayload)

	if !respPayload.Success {
		color.Red("Error popping stash:\n%s", respPayload.Output)
	} else {
		color.Green("--- Stash Pop Result ---")
		fmt.Print(respPayload.Output)
		color.Green("------------------------")
	}
}

func handleGitReset(stream network.Stream, repoAlias string) {
	color.Red("WARNING: This is a destructive operation. It will discard all uncommitted changes on the daemon.")
	fmt.Print("Are you sure you want to proceed? (y/n): ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(answer)
	if answer != "y" {
		fmt.Println("Reset aborted.")
		return
	}

	reqPayload := protocol.GitResetRequestPayload{RepoPath: repoAlias}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeGitResetRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, _ := protocol.ReadMessage(stream)
	var respPayload protocol.GitResetResponsePayload
	json.Unmarshal(resp.Payload, &respPayload)

	if !respPayload.Success {
		color.Red("Error from daemon: %s", respPayload.Output)
	} else {
		color.Green("--- Reset Result ---")
		fmt.Print(respPayload.Output)
		color.Green("--------------------")
	}
}

func printHelp() {
	fmt.Println("Available commands:")
	c := color.New(color.FgYellow)
	d := color.New(color.FgWhite)

	c.Println("  help          ", d.Sprint("Show this help message"))
	c.Println("  ls-repos      ", d.Sprint("List available repositories on the daemon"))
	c.Println("  use <repo>    ", d.Sprint("Switch context to a repository"))
	c.Println("  ls            ", d.Sprint("List files in the current repository"))
	c.Println("  cat <file>    ", d.Sprint("Display content of a remote file"))
	c.Println("  edit <file>   ", d.Sprint("Download, edit, and upload a file"))
	c.Println("  rename <old> <new> ", d.Sprint("Rename a remote file"))
	c.Println("  branch <name> ", d.Sprint("Create a new branch on the daemon"))
	c.Println("  commit <msg>  ", d.Sprint("Commit all changes in the repo and push to the current branch"))
	c.Println("  branches      ", d.Sprint("List branches in the current repository"))
	c.Println("  switch <name> ", d.Sprint("Switch to a different branch"))
	c.Println("  link <alias> <path>  ", d.Sprint("Dynamically link a new repository on the daemon"))
	c.Println("  status        ", d.Sprint("Show the working tree status on the daemon"))
	c.Println("  log           ", d.Sprint("Show recent commit history"))
	c.Println("  diff [file]   ", d.Sprint("Show changes between commits, commit and working tree, etc"))
	c.Println("  stash         ", d.Sprint("Stash changes in the current repository"))
	c.Println("  stash-pop     ", d.Sprint("Apply the most recent stash"))
	c.Println("  reset         ", d.Sprint("Discard all local changes (DESTRUCTIVE)"))
	c.Println("  exit, quit    ", d.Sprint("Close the application"))
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
		{Text: "branches", Description: "List branches in the current repository"},
		{Text: "switch", Description: "Switch to a different branch"},
		{Text: "link", Description: "Link a new repository on the daemon"},
		{Text: "status", Description: "Show the daemon's git status"},
		{Text: "log", Description: "Show recent commit history"},
		{Text: "diff", Description: "Show changes to files"},
		{Text: "stash", Description: "Stash changes in the current repository"},
		{Text: "stash-pop", Description: "Apply the most recent stash"},
		{Text: "reset", Description: "Discard all local changes (DESTRUCTIVE)"},
		{Text: "exit", Description: "Exit the shell"},
	}
	return prompt.FilterHasPrefix(s, d.GetWordBeforeCursor(), true)
}

func handleUseRepo(stream network.Stream, state *clientState, repoAlias string) {
	// We validate the repo by asking for its branches. If this succeeds, the repo exists.
	reqPayload := protocol.ListBranchesRequestPayload{RepoPath: repoAlias}
	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: protocol.TypeListBranchesRequest, Payload: payloadBytes}
	protocol.WriteMessage(stream, req)

	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		fmt.Printf("Error communicating with daemon: %v\n", err)
		return
	}

	var respPayload protocol.ListBranchesResponsePayload
	if err := json.Unmarshal(resp.Payload, &respPayload); err != nil {
		fmt.Printf("Error parsing response: %v\n", err)
		return
	}

	if !respPayload.Success {
		fmt.Printf("Error: Cannot use repo '%s'. Daemon responded: %s\n", repoAlias, respPayload.Error)
	} else {
		// Only change state on success!
		state.currentRepo = repoAlias
		fmt.Printf("Switched to repo: %s\n", state.currentRepo)
	}
}
