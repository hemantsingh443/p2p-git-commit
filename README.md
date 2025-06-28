# p2p-git-commit

**Note:** This project was originally prototyped in Rust, but switched to Go for easier cross-platform support and simpler libp2p integration.

A peer-to-peer Git remote and command relay system using Go and libp2p. This project enables secure, NAT-traversing Git operations and file access between devices without a central server.

## Features

- **Peer-to-peer Git remote**: Push, commit, and manage repositories over libp2p
- **Interactive REPL client**: SSH-like command interface with tab completion
- **Multi-repo support**: Manage multiple Git repositories through a single daemon
- **File operations**: List, read, edit, and rename files remotely (now using `git mv` for Git-aware renames)
- **Branch management**: Create, list, and switch between branches (with smart, stash-aware switching)
- **Dynamic repo linking**: Add repositories without restarting the daemon
- **Persistent identity**: Secure, repeatable identity with private keys
- **Trust model**: Handshake approval for new clients (like SSH)
- **Security**: Path traversal protection and repository aliasing
- **Debug logging**: Comprehensive logging for troubleshooting
- **Smart config manager**: One-time linking of daemons by name, with persistent config
- **Advanced stash and reset**: Stash (with untracked files), stash-pop, and destructive reset for recovery

## New: Smart Daemon Linking & Config Manager

You only need to handle the long multiaddress once per daemon. After linking, you can connect by name:

### Linking a New Daemon
```bash
./client link my-desktop
# Please scan or paste the multiaddress for 'my-desktop':
# (Paste the address from the daemon's console)
# Successfully linked 'my-desktop'. You can now connect using './client my-desktop'
```

### Connecting to a Linked Daemon
```bash
./client my-desktop
# Instantly connects using the saved address in ~/.p2p-git/config.json
```

### Config File Example
```json
{
  "my-desktop": "/ip4/192.168.1.100/tcp/4001/p2p/QmX...",
  "my-laptop": "/ip4/192.168.1.101/tcp/4001/p2p/QmY..."
}
```

## Advanced Workflow Features

- **Smart branch switching**: Automatically stashes work on the old branch and restores work for the new branch, so your changes follow your workflow intuitively.
- **Git-aware renames**: The `rename` command uses `git mv` to preserve file history and proper tracking.
- **Powerful stash**: The `stash` command includes untracked files, and `stash-pop` restores them.
- **Destructive reset**: The `reset` command (with confirmation) discards all local changes and resets the repo to the last commit—useful for escaping merge conflicts or stuck states.
- **Colorized output**: Errors, warnings, and results are color-coded for clarity.

## Example Usage

```bash
# Link a new daemon (one-time setup)
./client link my-desktop

# Connect to a daemon by name
./client my-desktop

# Rename a file (Git-aware)
rename old.txt new.txt

# Stash changes (including untracked)
stash

# Switch branches (auto-stash and restore)
switch feature-branch

# Pop the stash
stash-pop

# Destructive reset (with confirmation)
reset
```

## Multi-Repository Support

The system supports managing multiple Git repositories through a single daemon instance:

### Repository Management
- **Dynamic linking**: Add repositories on-the-fly with `link <alias> <path>`
- **Persistent storage**: Repositories saved to `linked_repos.json` for persistence
- **Context switching**: Use `use <repo-alias>` to switch between repositories
- **Repository listing**: `ls-repos` shows all available repositories

### Example Multi-Repo Workflow
```bash
# List available repositories
p2p-git(no repo)> ls-repos
--- Available Repositories ---
- my-project
- another-repo
- docs
------------------------------

# Switch to a repository
p2p-git(no repo)> use my-project
Switched to repo: my-project

# Work in my-project context
p2p-git(my-project @ main)> ls
README.md
src/main.go

# Add a new repository dynamically
p2p-git(my-project @ main)> link new-repo /path/to/new/repo
Successfully linked 'new-repo' on daemon.

# Switch to the new repository
p2p-git(my-project @ main)> use new-repo
Switched to repo: new-repo
```

## Recent Additions

- **Branch switching**: `switch <branch>` now actually changes the daemon's branch and auto-stashes/restores work
- **Dynamic repo linking**: `link <alias> <path>` adds repos without daemon restart
- **Smart validation**: Commands validate operations before changing client state
- **Persistent storage**: Repositories saved to `linked_repos.json` for persistence
- **Git-aware rename**: `rename` uses `git mv` for proper history
- **Stash improvements**: Stash includes untracked files
- **Destructive reset**: `reset` command for emergency recovery

## SSH-like Usage & Testing

The system is designed to feel like an SSH session for Git and file operations. Here's how you can use and test it:

### 1. Start the Daemon

```sh
./daemon -repo my-project:/absolute/path/to/your/repo
```
- The daemon will print a multiaddress and QR code for connecting clients.
- You can use relative paths, but the daemon will resolve them to absolute paths for reliability.

### 2. Connect with the Client (REPL)

```sh
./client /ip4/192.168.1.100/tcp/4001/p2p/QmDaemonID
```
- The first connection will require approval on the daemon (handshake, like SSH key acceptance).
- After approval, you get an interactive shell:

```
p2p-git(no repo)> ls-repos
- my-project
p2p-git(no repo)> use my-project
p2p-git(my-project @ master)> ls
README.md
src/main.go
...
p2p-git(my-project @ master)> cat README.md
# ... file content ...
p2p-git(my-project @ master)> edit src/main.go
# (opens in your $EDITOR, then uploads changes)
p2p-git(my-project @ master)> rename old.go new.go
Successfully renamed 'old.go' to 'new.go' on the daemon.
p2p-git(my-project @ master)> branch feature-x
Branch created successfully on daemon.
p2p-git(my-project @ feature-x)> commit "Add new feature"
--- Git Command Response from Daemon ---
Status: SUCCESS
Output:
Successfully pushed to origin/feature-x
...
```

### 3. Typical SSH-like Workflow

- **Connect**: `./client <daemon-multiaddress>`
- **List repos**: `ls-repos`
- **Switch repo**: `use <repo-alias>`
- **List files**: `ls`
- **View file**: `cat <file>`
- **Edit file**: `edit <file>` (opens in your $EDITOR, then uploads)
- **Rename file**: `rename <old> <new>`
- **Create branch**: `branch <name>`
- **Commit & push**: `commit <message>`
- **Tab completion**: Use <TAB> for command suggestions
- **Help**: `help`
- **Exit**: `exit` or `quit`

### 4. Security Notes
- All file operations are protected against path traversal.
- Only trusted clients (approved via handshake) can perform operations.
- All repo paths are resolved to absolute paths for reliability.

### 5. Troubleshooting
- The daemon logs all file and command requests, including the files found for `ls`.
- If `ls` shows zero files, check the daemon log for the absolute path being walked.
- If you see permission errors, ensure the daemon has access to the repo directory.

## Roadmap
- [x] Interactive REPL client
- [x] File listing, reading, editing, renaming
- [x] Branch creation and correct commit branch
- [x] Robust path handling
- [ ] Multi-repo support (multiple -repo flags)
- [ ] File upload/download (binary, large files)
- [ ] More granular permissions
- [ ] Mobile client

## Development & Testing
- All code is in Go, using libp2p for networking.
- To test, run the daemon and client on different machines or VMs, or use localhost for local testing.
- The workflow is similar to SSH: connect, operate, disconnect.

## Getting Started

### Prerequisites
- Go 1.20+
- Git installed on both devices

### Installation
Clone the repository:
```sh
git clone https://github.com/hemantsingh443/p2p-git-remote.git
cd p2p-git-remote
```
Build the daemon and client:
```sh
go build -o p2p-git-daemon ./cmd/daemon
go build -o p2p-git-client ./cmd/client
```

### Usage
#### 1. Start the Daemon (on your desktop/server)
```sh
./p2p-git-daemon -repo myrepo:/path/to/your/repo
```
- The daemon will print a QR code and a multiaddress. Scan the QR code with your mobile device or copy the address for the client.

#### 2. Connect with the Client (on your mobile/laptop)
```sh
./p2p-git-client -d <multiaddress-from-daemon>
```
- The first connection will prompt a handshake on the daemon. Approve it to trust the client.

#### 3. Push a Commit from the Client
```sh
./p2p-git-client -d <multiaddress> -repo myrepo -m "Your commit message" -b main
```

#### 4. List Available Repos
```sh
./p2p-git-client -d <multiaddress> --list-repos
```

#### 5. Read a File from a Repo
```sh
./p2p-git-client -d <multiaddress> --read-file myrepo:README.md
```

## License
MIT 