package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/hemantsingh443/p2p-git-remote/internal/protocol"
)

// Define constants for our different views
const (
	viewFiles = iota
	viewCommits
	viewBranches
)

// AppState holds the shared P2P state needed by the TUI.
type AppState struct {
	P2pHost       host.Host
	DaemonInfo    peer.AddrInfo
	CurrentRepo   string
	CurrentBranch string
}

// Model is the core state of our TUI application.
type Model struct {
	state      *AppState
	ready      bool
	statusMsg  string
	activePane int // 0 for nav, 1 for viewport

	// --- NEW: Multiple lists for the navigation pane ---
	navViews   []list.Model
	activeView int // The index of the currently visible list (viewFiles, etc.)

	// The content pane viewport
	viewport viewport.Model
	glamour  *glamour.TermRenderer // For syntax highlighting

	// --- NEW STATE ---
	isInputting      bool            // Are we currently typing a commit message?
	textInput        textinput.Model // The input field for commit messages
	afterInputAction tea.Cmd         // What to do after input is done (e.g., commit)
}

// --- Bubble Tea Interface Implementation ---

func NewModel(state *AppState) Model {
	// --- Setup our three lists ---
	fileList := list.New([]list.Item{}, itemDelegate{}, 0, 0)
	fileList.Title = "Files"

	commitList := list.New([]list.Item{}, itemDelegate{}, 0, 0)
	commitList.Title = "Commits"

	branchList := list.New([]list.Item{}, itemDelegate{}, 0, 0)
	branchList.Title = "Branches"

	// Setup the Glamour renderer for syntax highlighting
	glamourRenderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(0), // We let the viewport handle wrapping
	)

	// --- NEW: Initialize TextInput ---
	ti := textinput.New()
	ti.Placeholder = "Commit message..."
	ti.Focus()
	ti.CharLimit = 156
	ti.Width = 80

	m := Model{
		state:       state,
		navViews:    []list.Model{fileList, commitList, branchList},
		activeView:  viewFiles, // Start with the file view
		statusMsg:   "Loading...",
		activePane:  0,
		glamour:     glamourRenderer,
		isInputting: false,
		textInput:   ti,
	}

	// Set initial titles, including the branch
	(&m).updateTitles()
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchListContent(m.state, viewFiles),
		fetchListContent(m.state, viewCommits),
		fetchListContent(m.state, viewBranches),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// --- NEW: Handle input mode separately ---
	if m.isInputting {
		var cmd tea.Cmd
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "enter":
				// Commit with the message
				commitMsg := m.textInput.Value()
				m.isInputting = false
				m.textInput.Reset()
				return m, commitCmd(m.state, commitMsg)
			case "ctrl+c", "esc":
				// Cancel input
				m.isInputting = false
				m.textInput.Reset()
				m.statusMsg = "Commit cancelled."
				return m, nil
			}
		}
		m.textInput, cmd = m.textInput.Update(msg)
		return m, cmd
	}
	// ... rest of the Update function as before ...
	var cmds []tea.Cmd
	var cmd tea.Cmd
	oldIndex := m.navViews[m.activeView].Index()
	m.navViews[m.activeView], cmd = m.navViews[m.activeView].Update(msg)
	cmds = append(cmds, cmd)
	if m.navViews[m.activeView].Index() != oldIndex {
		if m.navViews[m.activeView].SelectedItem() != nil {
			selectedItem := m.navViews[m.activeView].SelectedItem().(item)
			cmds = append(cmds, m.fetchContent(m.state, "cat", string(selectedItem)))
		}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h, v := appStyle.GetFrameSize()
		for i := range m.navViews {
			m.navViews[i].SetSize(msg.Width/3-h, msg.Height-v-3)
		}
		m.viewport.Width = msg.Width*2/3 - h
		m.viewport.Height = msg.Height - v - 3
		m.ready = true
	case listLoadedMsg:
		m.navViews[msg.viewIndex].SetItems(msg.items)
	case contentReadyMsg:
		m.viewport.SetContent(msg.content)
		m.statusMsg = msg.status
	case errorMsg:
		m.statusMsg = "Error: " + msg.err.Error()
	case branchSwitchedMsg:
		m.state.CurrentBranch = msg.branchName // Solidify the state
		m.statusMsg = fmt.Sprintf("Successfully switched to branch: %s", msg.branchName)
		// Refresh all lists to reflect the new branch state
		return m, tea.Batch(
			fetchListContent(m.state, viewFiles),
			fetchListContent(m.state, viewCommits),
		)
	case tea.KeyMsg:
		if m.navViews[m.activeView].FilterState() == list.Filtering {
			return m, tea.Batch(cmds...)
		}
		switch msg.String() {
		case "1":
			m.activeView = viewFiles
			m.navViews[m.activeView].Title = "> Files"
			m.navViews[viewCommits].Title = "  Commits"
			m.navViews[viewBranches].Title = "  Branches"
		case "2":
			m.activeView = viewCommits
			m.navViews[m.activeView].Title = "> Commits"
			m.navViews[viewFiles].Title = "  Files"
			m.navViews[viewBranches].Title = "  Branches"
		case "3":
			m.activeView = viewBranches
			m.navViews[m.activeView].Title = "> Branches"
			m.navViews[viewFiles].Title = "  Files"
			m.navViews[viewCommits].Title = "  Commits"
		case "e":
			if m.activeView == viewFiles && m.navViews[viewFiles].SelectedItem() != nil {
				selectedItem := m.navViews[viewFiles].SelectedItem().(item)
				return m, editFileCmd(m.state, string(selectedItem))
			}
		case "S":
			m.statusMsg = "Stashing changes..."
			return m, stashCmd(m.state)
		case "C":
			m.isInputting = true
			m.statusMsg = "Enter commit message (enter to confirm, esc to cancel)"
			return m, nil
		case "?":
			m.statusMsg = "1-3:Views|S:Stash|C:Commit|s:status|l:log|q:quit"
		case "enter":
			if m.activePane == 0 && m.navViews[m.activeView].SelectedItem() != nil {
				selectedItem := m.navViews[m.activeView].SelectedItem().(item)
				switch m.activeView {
				case viewFiles:
					return m, m.fetchContent(m.state, "diff", string(selectedItem))
				case viewBranches:
					branchName := string(selectedItem)
					if branchName == m.state.CurrentBranch {
						return m, nil
					}
					// --- FIX #2: Optimistic UI Update ---
					m.state.CurrentBranch = branchName // Tentatively set the new branch
					m.updateTitles()                   // Re-render the titles NOW
					m.statusMsg = "Switching to branch " + branchName + "..."
					// Now, send the command to the daemon to make it official.
					// If it fails, the errorMsg will let the user know.
					return m, switchBranchCmd(m.state, branchName)
				}
			}
		}
	}
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	listStyle := paneStyle
	viewportStyle := paneStyle
	if m.activePane == 0 {
		listStyle = activePaneStyle
	} else {
		viewportStyle = activePaneStyle
	}

	// --- RENDER THE ACTIVE LIST ---
	navView := listStyle.Render(m.navViews[m.activeView].View())
	contentView := viewportStyle.Render(m.viewport.View())

	mainView := lipgloss.JoinHorizontal(lipgloss.Top, navView, contentView)
	statusBar := statusBarStyle.Render(m.statusMsg)

	// --- NEW: Render input box if active ---
	if m.isInputting {
		// Overlay the input box on top of the main view
		return lipgloss.JoinVertical(lipgloss.Left, mainView, m.textInput.View(), statusBar)
	}
	return lipgloss.JoinVertical(lipgloss.Left, mainView, statusBar)
}

// --- Helper Types for Bubble Tea ---

// item represents a file in our list.
type item string

func (i item) FilterValue() string { return string(i) }
func (i item) Title() string       { return string(i) }
func (i item) Description() string { return "" }

// --- NEW: Custom Delegate for the list ---
type itemDelegate struct{}

func (d itemDelegate) Height() int                               { return 1 }
func (d itemDelegate) Spacing() int                              { return 0 }
func (d itemDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(item)
	if !ok {
		return
	}
	str := string(i)
	fn := func(s string) string {
		return lipgloss.NewStyle().Padding(0, 0, 0, 2).Render(s)
	}
	if index == m.Index() {
		fn = func(s string) string {
			return lipgloss.NewStyle().
				Padding(0, 0, 0, 1).
				Foreground(lipgloss.Color("63")).
				Render("> " + s)
		}
	}
	fmt.Fprint(w, fn(str))
}

// Messages are used to communicate between our async commands and the Update function.
type listLoadedMsg struct {
	viewIndex int
	items     []list.Item
}
type contentReadyMsg struct{ content, status string }
type errorMsg struct{ err error }

// --- Commands for Async P2P Operations ---

func fetchListContent(state *AppState, viewIndex int) tea.Cmd {
	return func() tea.Msg {
		var reqType string
		var reqPayload interface{}

		switch viewIndex {
		case viewFiles:
			reqType = protocol.TypeListFilesRequest
			reqPayload = protocol.ListFilesRequestPayload{RepoPath: state.CurrentRepo}
		case viewCommits:
			reqType = protocol.TypeGitLogRequest // We reuse the log response
			reqPayload = protocol.GitLogRequestPayload{RepoPath: state.CurrentRepo}
		case viewBranches:
			reqType = protocol.TypeListBranchesRequest
			reqPayload = protocol.ListBranchesRequestPayload{RepoPath: state.CurrentRepo}
		}

		respBytes, err := sendRequest(state, reqType, reqPayload)
		if err != nil {
			return errorMsg{err}
		}

		var items []list.Item
		switch viewIndex {
		case viewFiles:
			var p protocol.ListFilesResponsePayload
			json.Unmarshal(respBytes, &p)
			for _, file := range p.Files {
				items = append(items, item(file))
			}
		case viewCommits:
			var p protocol.GitLogResponsePayload
			json.Unmarshal(respBytes, &p)
			// Split the log output into individual lines for the list
			lines := strings.Split(p.Output, "\n")
			for _, line := range lines {
				if line != "" {
					items = append(items, item(line))
				}
			}
		case viewBranches:
			var p protocol.ListBranchesResponsePayload
			json.Unmarshal(respBytes, &p)
			for _, branch := range p.Branches {
				items = append(items, item(branch))
			}
		}
		return listLoadedMsg{viewIndex: viewIndex, items: items}
	}
}

func (m *Model) fetchContent(state *AppState, command, filePath string) tea.Cmd {
	return func() tea.Msg {
		var reqType string
		var reqPayload interface{}
		var statusMsg string

		switch command {
		case "diff":
			reqType = protocol.TypeGitDiffRequest
			reqPayload = protocol.GitDiffRequestPayload{RepoPath: state.CurrentRepo, FilePath: filePath}
			statusMsg = fmt.Sprintf("Showing diff for %s...", filePath)
		case "status":
			reqType = protocol.TypeGitStatusRequest
			reqPayload = protocol.GitStatusRequestPayload{RepoPath: state.CurrentRepo}
			statusMsg = "Showing git status..."
		case "log":
			reqType = protocol.TypeGitLogRequest
			reqPayload = protocol.GitLogRequestPayload{RepoPath: state.CurrentRepo}
			statusMsg = "Showing git log..."
		case "cat":
			reqType = protocol.TypeReadFileRequest
			reqPayload = protocol.ReadFileRequestPayload{RepoPath: state.CurrentRepo, FilePath: filePath}
			statusMsg = fmt.Sprintf("Showing content for %s...", filePath)
		default:
			return errorMsg{fmt.Errorf("unknown TUI command: %s", command)}
		}

		respBytes, err := sendRequest(state, reqType, reqPayload)
		if err != nil {
			return errorMsg{err}
		}

		// --- THIS IS THE CRITICAL FIX ---
		// Now we unmarshal specifically for each type and check for success.
		var output string
		switch command {
		case "diff":
			var p protocol.GitDiffResponsePayload
			json.Unmarshal(respBytes, &p)
			if !p.Success {
				return errorMsg{fmt.Errorf(p.Output)}
			}
			output = p.Output
		case "status":
			var p protocol.GitStatusResponsePayload
			json.Unmarshal(respBytes, &p)
			if !p.Success {
				return errorMsg{fmt.Errorf(p.Output)}
			}
			output = p.Output
		case "log":
			var p protocol.GitLogResponsePayload
			json.Unmarshal(respBytes, &p)
			if !p.Success {
				return errorMsg{fmt.Errorf(p.Output)}
			}
			output = p.Output
		case "cat":
			var p protocol.ReadFileResponsePayload
			json.Unmarshal(respBytes, &p)
			if !p.Success {
				return errorMsg{fmt.Errorf(p.Error)}
			}
			output = p.Content
		}
		// --- NEW: Apply Syntax Highlighting ---
		var finalContent string
		var errHighlight error
		switch command {
		case "diff":
			finalContent, errHighlight = m.glamour.Render("```diff\n" + output + "\n```")
		case "log":
			finalContent = output
		case "cat":
			finalContent, errHighlight = m.glamour.Render("```go\n" + output + "\n```")
		default:
			finalContent = output
		}
		if errHighlight != nil {
			return errorMsg{errHighlight}
		}
		return contentReadyMsg{content: finalContent, status: statusMsg}
	}
}

// sendRequest is a generic helper to reduce code duplication.
func sendRequest(state *AppState, reqType string, reqPayload interface{}) (json.RawMessage, error) {
	stream, err := state.P2pHost.NewStream(context.Background(), state.DaemonInfo.ID, protocol.ProtocolID)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	payloadBytes, _ := json.Marshal(reqPayload)
	req := &protocol.Message{Type: reqType, Payload: payloadBytes}
	if err := protocol.WriteMessage(stream, req); err != nil {
		return nil, err
	}

	resp, err := protocol.ReadMessage(stream)
	if err != nil {
		return nil, err
	}
	return resp.Payload, nil
}

// --- Lipgloss Styling ---

var (
	appStyle  = lipgloss.NewStyle().Padding(1, 2)
	paneStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1)
	activePaneStyle = paneStyle.Copy().
			Border(lipgloss.ThickBorder()).
			BorderForeground(lipgloss.Color("63")) // A nice purple
	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("250")).
			Padding(0, 1)
)

// This command quits the TUI, runs the editor, and then needs the app to be restarted.
// A more advanced version would use tea.Exec to handle this gracefully.
func editFileCmd(state *AppState, filePath string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	c := exec.Command(editor, filePath)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return errorMsg{err}
		}
		return contentReadyMsg{content: "Finished editing " + filePath, status: "Edit complete."}
	})
}

// This helper method updates the titles of the panes to reflect the current state
func (m *Model) updateTitles() {
	branchTitle := fmt.Sprintf(" Branch: %s ", m.state.CurrentBranch)
	m.navViews[viewFiles].Title = "Files"
	m.navViews[viewCommits].Title = "Commits"
	m.navViews[viewBranches].Title = "Branches"
	// Mark the active view with a > and show the current branch
	m.navViews[m.activeView].Title = "> " + m.navViews[m.activeView].Title + branchTitle
}

// This command tells the daemon to switch branches
func switchBranchCmd(state *AppState, branchName string) tea.Cmd {
	return func() tea.Msg {
		reqPayload := protocol.SwitchBranchRequestPayload{
			RepoPath:   state.CurrentRepo,
			BranchName: branchName,
		}
		respBytes, err := sendRequest(state, protocol.TypeSwitchBranchRequest, reqPayload)
		if err != nil {
			return errorMsg{err}
		}
		var p protocol.SwitchBranchResponsePayload
		json.Unmarshal(respBytes, &p)
		if !p.Success {
			return errorMsg{fmt.Errorf(p.Output)}
		}
		// On success, return a message that includes the new branch name
		return branchSwitchedMsg{branchName: branchName}
	}
}

// Add a new message type for successful switches
type branchSwitchedMsg struct{ branchName string }

// Create commands for stash and commit
func stashCmd(state *AppState) tea.Cmd {
	return func() tea.Msg {
		reqPayload := protocol.GitStashSaveRequestPayload{RepoPath: state.CurrentRepo}
		respBytes, err := sendRequest(state, protocol.TypeGitStashSaveRequest, reqPayload)
		if err != nil {
			return errorMsg{err}
		}
		var p protocol.GitStashSaveResponsePayload
		json.Unmarshal(respBytes, &p)
		if !p.Success {
			return errorMsg{fmt.Errorf(p.Output)}
		}
		// On success, refresh the file list to show the clean state
		return tea.Batch(
			func() tea.Msg { return contentReadyMsg{content: p.Output, status: "Stash successful."} },
			fetchListContent(state, viewFiles),
		)()
	}
}

func commitCmd(state *AppState, message string) tea.Cmd {
	return func() tea.Msg {
		reqPayload := protocol.GitCommitRequestPayload{
			RepoPath: state.CurrentRepo,
			Message:  message,
			Branch:   state.CurrentBranch,
		}
		respBytes, err := sendRequest(state, protocol.TypeGitCommitRequest, reqPayload)
		if err != nil {
			return errorMsg{err}
		}
		var p protocol.GitCommitResponsePayload
		json.Unmarshal(respBytes, &p)
		if !p.Success {
			return errorMsg{fmt.Errorf(p.Output)}
		}
		// On success, refresh everything
		return tea.Batch(
			func() tea.Msg { return contentReadyMsg{content: p.Output, status: "Commit successful."} },
			fetchListContent(state, viewFiles),
			fetchListContent(state, viewCommits),
		)()
	}
}
