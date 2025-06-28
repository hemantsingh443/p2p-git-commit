# p2p-git-remote

A peer-to-peer Git remote and command relay system using Go and libp2p. This project enables secure, NAT-traversing Git operations and file access between devices without a central server.

## Features

- **Peer-to-peer Git remote**: Push, commit, and manage repositories over libp2p
- **Persistent identity**: Both client and daemon use persistent private keys for secure, repeatable identity
- **Trust model**: Daemon requires handshake approval for new clients (like SSH key acceptance)
- **Repository listing**: List all repositories linked to the daemon (`ls-repos`)
- **File listing**: List all files in a repository, including files in the root and subdirectories (`ls`)
- **File reading**: View the content of any file in the repository (`cat <file>`) 
- **File editing**: Download, edit locally in your favorite editor, and upload changes back to the daemon (`edit <file>`) 
- **File renaming**: Rename files in the repository (`rename <old> <new>`) 
- **Branch creation**: Create new branches on the daemon (`branch <name>`) 
- **Robust commit and push**: Commits are always made to the correct branch (client context is respected)
- **Tab completion and help**: Interactive REPL client with command completion and contextual help
- **Security**: Path traversal protection, explicit trust, and repository aliasing
- **Debug logging**: Daemon logs all file operations and requests for easy troubleshooting

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

---

**Note:** This project was originally prototyped in Rust, but switched to Go for easier cross-platform support and simpler libp2p integration.

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