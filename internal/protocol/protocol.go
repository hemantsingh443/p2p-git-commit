package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"

	"github.com/libp2p/go-libp2p/core/network"
)

// ProtocolID is the unique identifier for our protocol.
const ProtocolID = "/p2p-git-remote/1.0.0"

// Message defines the structure of our communication messages.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Payloads for specific message types
type HandshakeResponsePayload struct {
	Approved bool `json:"approved"`
}

type GitCommitRequestPayload struct {
	RepoPath string `json:"repo_path"`
	Message  string `json:"message"`
	Branch   string `json:"branch"`
}

type GitCommitResponsePayload struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
}

// --- NEW MESSAGE TYPES ---
const (
	TypeHandshakeRequest  = "HANDSHAKE_REQUEST"
	TypeHandshakeResponse = "HANDSHAKE_RESPONSE"
	TypeGitCommitRequest  = "GIT_COMMIT_REQUEST"
	TypeGitCommitResponse = "GIT_COMMIT_RESPONSE"

	// New for repo listing
	TypeListReposRequest  = "LIST_REPOS_REQUEST"
	TypeListReposResponse = "LIST_REPOS_RESPONSE"

	// New for file editing
	TypeReadFileRequest   = "READ_FILE_REQUEST"
	TypeReadFileResponse  = "READ_FILE_RESPONSE"
	TypeWriteFileRequest  = "WRITE_FILE_REQUEST"
	TypeWriteFileResponse = "WRITE_FILE_RESPONSE"
)

// New Payloads
type ListReposResponsePayload struct {
	Repos []string `json:"repos"`
}

type ReadFileRequestPayload struct {
	RepoPath string `json:"repo_path"`
	FilePath string `json:"file_path"` // e.g., "README.md" or "src/main.go"
}

type ReadFileResponsePayload struct {
	Success bool   `json:"success"`
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

type WriteFileRequestPayload struct {
	RepoPath string `json:"repo_path"`
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

type WriteFileResponsePayload struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// ReadMessage reads a JSON message from a stream.
func ReadMessage(stream network.Stream) (*Message, error) {
	var msg Message
	err := json.NewDecoder(stream).Decode(&msg)
	if err != nil {
		return nil, fmt.Errorf("failed to decode message: %w", err)
	}
	return &msg, nil
}

// WriteMessage writes a JSON message to a stream.
func WriteMessage(stream network.Stream, msg *Message) error {
	// Create a buffered writer. This gives us control over flushing.
	writer := bufio.NewWriter(stream)

	// Encode the message into the buffer.
	err := json.NewEncoder(writer).Encode(msg)
	if err != nil {
		return fmt.Errorf("failed to encode message: %w", err)
	}

	// Flush the buffer, which sends all the data over the network.
	// This is the critical step that breaks the deadlock.
	err = writer.Flush()
	if err != nil {
		return fmt.Errorf("failed to flush stream: %w", err)
	}

	return nil
}
