# p2p-git-remote

A peer-to-peer (P2P) Git remote and command relay using libp2p, enabling secure, NAT-traversing, and trust-based Git operations and file access between devices (e.g., desktop and mobile) without a central server.

> **Note:** This project is now implemented in Go. Rust libp2p version control was too much to deal with, and creating an SDK for android made it 10 times more tedious.

## Features
- **P2P Git Remote:** Push commits to your desktop from a mobile device or another computer over libp2p.
- **NAT Traversal:** Uses hole punching and relay to work across networks and firewalls.
- **Trust Model:** Only trusted peers can execute commands; handshake required for new peers.
- **QR Code Onboarding:** Scan a QR code to connect a new device.
- **File Operations:** List repos, read, and (planned) write files remotely.

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

## Security Notes
- **Trust Model:** Only trusted peers can execute commands. Handshake approval is required for new peers.
- **Path Traversal Protection:** File read/write operations are protected against path traversal attacks.
- **Private Keys:** Each peer generates and stores a persistent private key for identity.
- **Do not expose the daemon to untrusted networks.**

## Roadmap
- [x] Git commit/push relay
- [x] Repo listing
- [x] File read
- [ ] File write/edit
- [ ] Mobile UI client
- [ ] Multi-repo support in one daemon

## License
MIT 