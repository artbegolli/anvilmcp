# Claude Code Configuration Guide

This document explains the Claude Code settings configured in `settings.json`.

---

## Overview

The configuration provides:
- ✅ **Full git operations** (commit, push, pull, merge, rebase, etc.)
- ✅ **Full Go operations** (build, test, fmt, vet, run, etc.)
- ✅ **Safe file operations** (read, write, create, move, copy)
- ✅ **Build automation** (make, build, test)
- ❌ **Sensitive operations denied** (rm, dd, sudo, ssh, curl POST/PUT/DELETE)
- ❌ **Restricted directories** (/etc, /root, ~/.ssh, ~/.aws, etc.)

---

## Configuration Sections

### 1. Permissions (Allowlist & Denylist)

#### Allowed Commands

**Git Operations**:
```
git commit      - Create commits
git push        - Push to remote
git pull        - Pull from remote
git merge       - Merge branches
git rebase      - Rebase commits
git branch      - Manage branches
git checkout    - Switch branches
git stash       - Stash changes
git log         - View history
git diff        - View changes
git status      - Check status
git show        - Show commits
git tag         - Manage tags
git add         - Stage files
git reset       - Unstage files
```

**Go Operations**:
```
go build        - Build binary
go test         - Run tests
go fmt          - Format code
go vet          - Lint code
go run          - Run program
go install      - Install binary
go get          - Fetch dependencies
go mod          - Manage modules
```

**Build Tools**:
```
make            - Run makefile targets
make test       - Run tests
make build      - Build project
make clean      - Clean build artifacts
```

**File Operations**:
```
find            - Find files
grep            - Search content
ls              - List files
cat             - Display files
head/tail       - View file parts
wc              - Count lines
sed/awk         - Text processing
echo            - Print text
mkdir           - Create directories
touch           - Create files
cp              - Copy files
mv              - Move files
chmod/chown     - Change permissions
pwd/cd          - Directory navigation
tree            - Tree view
```

#### Denied Commands

**Dangerous Operations**:
```
rm/rmdir        - Delete files/dirs (DANGEROUS)
dd              - Disk operations (DANGEROUS)
mkfs            - Format filesystem (DANGEROUS)
shred           - Securely delete (DANGEROUS)
fsck            - Filesystem check (DANGEROUS)
```

**System Operations**:
```
mount/umount    - Mount filesystems
reboot/shutdown - Restart system
kill/pkill      - Kill processes
sudo            - Escalate privileges
passwd          - Change passwords
useradd/userdel - User management
groupadd/del    - Group management
chroot          - Change root
iptables        - Firewall rules
semanage        - SELinux policy
```

**Network Operations**:
```
curl POST/PUT/DELETE - HTTP mutations (only GET/HEAD allowed)
wget            - Download files
ssh/scp         - Remote shell
sftp            - Remote file transfer
telnet          - Remote login
nc/netcat       - Network communication
ftp             - File transfer
openssl         - SSL operations
```

**Editor Operations**:
```
vi/vim/nano     - Interactive editors (won't work anyway)
visudo          - Edit sudoers
```

---

### 2. Git Configuration

```json
"git": {
  "allowPush": true,                    // Allow git push
  "allowForcePush": false,              // Prevent force push (safety)
  "requireCommitMessage": true,         // Require detailed messages
  "defaultAuthor": {                    // Default commit author
    "name": "Claude Code",
    "email": "noreply@anthropic.com"
  }
}
```

**Features**:
- ✅ Normal push allowed
- ❌ Force push prevented (prevents accidents)
- ✅ Commit messages required
- ✅ All commits attributed to Claude Code

---

### 3. Go Configuration

```json
"golang": {
  "allowModuleChanges": true,           // Allow go.mod updates
  "allowBuildModifications": true,      // Allow build changes
  "testOnBuild": true                   // Run tests during builds
}
```

**Features**:
- ✅ Can update dependencies (go get)
- ✅ Can modify build (go build flags)
- ✅ Automatic testing on builds

---

### 4. File Operations

```json
"fileOperations": {
  "allowedDirectories": ["./"],         // Only project root
  "deniedDirectories": [
    "/etc",                             // System config
    "/sys", "/proc",                    // Kernel interfaces
    "/root",                            // Root home
    "/home",                            // User homes
    "~/.ssh",                           // SSH keys
    "~/.aws",                           // AWS credentials
    "~/.azure",                         // Azure credentials
    "~/.gnupg",                         // GPG keys
    "~/.kube",                          // Kubernetes config
    "/var/log"                          // System logs
  ],
  "deniedFiles": [
    ".env",                             // Environment files
    ".env.local",
    ".env.*.local",
    "secrets.json",                     // Secrets
    "credentials.json",
    ".git/config",                      // Git config
    ".github/workflows",                // CI/CD config
    "*.pem", "*.key",                   // Certificates
    "*.pfx", "*.p12"
  ]
}
```

**Protection**:
- Restricted to project directory only
- System directories blocked
- Sensitive directories blocked
- Secret files blocked
- Certificate files blocked

---

### 5. Limits

```json
"limits": {
  "commandTimeout": 300000,             // 5 minutes
  "maxConcurrentCommands": 5,           // Max 5 parallel
  "maxOutputSize": 10485760             // 10 MB max output
}
```

**Features**:
- Commands timeout after 5 minutes (prevents hangs)
- Max 5 concurrent commands (prevents resource exhaustion)
- Max 10 MB output per command (prevents memory issues)

---

### 6. Safety Features

```json
"safety": {
  "confirmDestructiveOps": true,        // Confirm before destructive ops
  "confirmFileDeletes": true,           // Confirm before file deletion
  "preventForcePush": true,             // Block force push
  "preventResetHard": true,             // Block reset --hard
  "preventHistoryRewrite": true         // Block history rewrites
}
```

**Protections**:
- ✅ Confirmation required for destructive operations
- ✅ Confirmation required for file deletions
- ✅ Force push prevented
- ✅ Hard reset prevented (won't lose commits)
- ✅ History rewrites prevented

---

### 7. Hooks

```json
"hooks": {
  "beforeGitPush": {
    "enabled": true,
    "commands": ["git log -1 --pretty=format:%s"]
  },
  "afterGitCommit": {
    "enabled": true,
    "commands": ["git log -1"]
  }
}
```

**Purpose**:
- Before push: Shows what's being pushed
- After commit: Shows the commit created

---

### 8. Logging

```json
"logging": {
  "enabled": true,
  "logLevel": "info",
  "logFile": ".claude/claude.log"
}
```

**Logging**:
- All operations logged to `.claude/claude.log`
- Useful for debugging
- Review for security audit

---

## Usage Scenarios

### ✅ Allowed Workflows

**Making Code Changes**:
```bash
# Edit files
vim src/main.go          # ❌ Won't work (editor blocked)
# But Claude can write directly:
# - Read file
# - Modify
# - Write back

# Test changes
make test                # ✅ Allowed
go test ./...            # ✅ Allowed
go fmt ./...             # ✅ Allowed
go vet ./...             # ✅ Allowed

# Commit changes
git add .                # ✅ Allowed
git commit -m "fix: ..."  # ✅ Allowed
git push                 # ✅ Allowed
```

**Building Project**:
```bash
make build               # ✅ Allowed
go build ./cmd/...       # ✅ Allowed
go install ./...         # ✅ Allowed
```

**Searching Code**:
```bash
grep -r "TODO" .         # ✅ Allowed
find . -name "*.go"      # ✅ Allowed
```

### ❌ Denied Workflows

**Deleting Files**:
```bash
rm file.txt              # ❌ Denied
rm -rf directory/        # ❌ Denied
```
**Workaround**: Use git to delete tracked files:
```bash
git rm file.txt          # ✅ Allowed
```

**Remote Access**:
```bash
ssh user@server.com      # ❌ Denied
scp file user@server     # ❌ Denied
```

**System Changes**:
```bash
sudo apt-get install pkg # ❌ Denied
chmod 777 file           # Allowed but restricted
```

**Network Requests (Mutations)**:
```bash
curl -X POST ...         # ❌ Denied
curl -X PUT ...          # ❌ Denied
curl -X DELETE ...       # ❌ Denied
curl -X GET ...          # ✅ Allowed (read-only)
```

---

## Customization

To modify this configuration:

1. **Edit `.claude/settings.json`**
2. **Add/remove from allowlist or denylist**
3. **Changes apply immediately**

### Example: Add a Custom Command

To allow a new command, add to the allowlist:
```json
"bash": [
  "git commit",
  // ... existing entries ...
  "my-custom-script"    // Add new command
]
```

### Example: Block a Command

To block a command, add to the denylist:
```json
"bash": [
  "rm",
  // ... existing entries ...
  "my-dangerous-command" // Block new command
]
```

---

## Best Practices

### ✅ Do

- ✅ Use `git` for file deletions instead of `rm`
- ✅ Run `make test` before committing
- ✅ Use `go fmt` and `go vet`
- ✅ Write detailed commit messages
- ✅ Review changes before pushing
- ✅ Use branches for new features

### ❌ Don't

- ❌ Try to use `rm` (it's blocked)
- ❌ Try to use `sudo` (it's blocked)
- ❌ Try to use `ssh` (it's blocked)
- ❌ Try to force push (it's blocked)
- ❌ Try to access /etc or /root
- ❌ Try to access ~/.ssh or ~/.aws

---

## Security Notes

### What This Protects Against

1. **Accidental Deletion**: `rm` is blocked
2. **Privilege Escalation**: `sudo` is blocked
3. **Remote Access**: `ssh`/`scp` are blocked
4. **Credential Exposure**: Sensitive dirs/files blocked
5. **History Loss**: Hard reset prevented
6. **Force Push**: Blocked to prevent overwrites
7. **Network Mutation**: POST/PUT/DELETE blocked
8. **System Damage**: System commands blocked

### What This Does NOT Protect Against

1. **Code Injection**: If code itself is malicious
2. **Logic Errors**: If git commands are used incorrectly
3. **Committed Secrets**: If secrets are committed
4. **Dependency Vulnerabilities**: If go modules have vulns
5. **Network Reads**: curl GET is still allowed

---

## Troubleshooting

### "Command not allowed"

**Problem**: Getting "command not allowed" error

**Solution**:
1. Check if the command is in the denylist
2. Use an alternative command (e.g., `git rm` instead of `rm`)
3. Request it be added to allowlist

### "Permission denied"

**Problem**: Getting permission denied error

**Solution**:
1. Check the file/directory is not in denied list
2. Ensure you're in the project root
3. Check file permissions with `ls -la`

### Command timeout

**Problem**: Command takes too long (> 5 min)

**Solution**:
1. Increase timeout in settings.json
2. Optimize the command
3. Break into smaller operations

---

## Security Review

This configuration was designed with:
- ✅ **Principle of Least Privilege**: Only necessary commands allowed
- ✅ **Defense in Depth**: Multiple layers (blocklist + allowlist)
- ✅ **Safe Defaults**: Deny by default, allow specific operations
- ✅ **Logging**: All operations logged
- ✅ **Immutability**: Prevents destructive operations

---

## Questions?

For questions about the configuration, see:
- `settings.json` - Current configuration
- Claude Code documentation
- Git best practices guide

---

**Configuration Version**: 1.0  
**Last Updated**: 2026-05-16  
**Status**: ✅ Ready for use

