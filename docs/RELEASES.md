# Releases and GitHub Actions

This document explains how AnvilMCP uses GitHub Actions for building, testing, and releasing binaries.

## Workflows

### 1. Test Workflow (`test.yml`)

**Triggers:**
- On every push to `main`
- On every pull request to `main`

**Jobs:**
- **Test**: Runs tests on Ubuntu and macOS with Go 1.22
  - `go test -v -race ./...`
  - Uploads coverage to Codecov
- **Lint**: Runs golangci-lint
  - Checks code quality
  - Enforces style standards
- **Security**: Runs Gosec security scanner
  - Scans for security vulnerabilities
  - Uploads results to GitHub Security tab

### 2. Build and Release Workflow (`build-and-release.yml`)

**Triggers:**
- When a tag is created matching `v*.*.*` (e.g., `v1.0.0`)
- Manual trigger via workflow_dispatch (for ad-hoc releases)

**Jobs:**

**Build** - Compiles binaries for multiple platforms:
- Linux: amd64, arm64, arm
- macOS: amd64, arm64
- Windows: amd64

**Test** - Runs full test suite with race detector

**Release** - Creates GitHub release with:
- All compiled binaries
- SHA256 checksums
- Auto-generated release notes

**Docker** - Builds and optionally pushes Docker image:
- Builds multi-platform Docker image
- Tests image locally
- Optionally pushes to Docker Hub (if credentials configured)

## Creating a Release

### Method 1: Using Git Tags (Recommended)

1. **Update version in code** (if version is hardcoded):
   ```bash
   # Update any version constants or documentation
   vi cmd/anvilmcp/main.go  # If there's a version constant
   git add .
   git commit -m "Bump version to 1.0.0"
   ```

2. **Create annotated tag**:
   ```bash
   git tag -a v1.0.0 -m "Release version 1.0.0"
   ```

3. **Push tag to GitHub**:
   ```bash
   git push origin v1.0.0
   ```

4. **Watch the workflow**:
   - Go to GitHub Actions tab
   - Watch the "Build and Release" workflow run
   - Binaries will be automatically uploaded to Releases

### Method 2: Manual Workflow Dispatch

1. **Go to Actions tab** in GitHub
2. **Select "Build and Release"** workflow
3. **Click "Run workflow"**
4. **Enter version** (e.g., `v1.0.0`)
5. **Confirm and run**

## Release Contents

Each release includes:

```
anvilmcp-v1.0.0-linux-amd64
anvilmcp-v1.0.0-linux-arm64
anvilmcp-v1.0.0-linux-arm
anvilmcp-v1.0.0-darwin-amd64
anvilmcp-v1.0.0-darwin-arm64
anvilmcp-v1.0.0-windows-amd64.exe
SHA256SUMS
```

### Verifying Binary Integrity

**Check SHA256 checksums:**
```bash
# Download both binary and SHA256SUMS from release
sha256sum -c SHA256SUMS

# Or manually verify:
sha256sum anvilmcp-v1.0.0-linux-amd64
# Compare with value in SHA256SUMS
```

## Using Released Binaries

### Linux/macOS

```bash
# Download from release page or via curl
curl -L -O https://github.com/artbegolli/anvilmcp/releases/download/v1.0.0/anvilmcp-v1.0.0-linux-amd64

# Make executable
chmod +x anvilmcp-v1.0.0-linux-amd64

# Run
./anvilmcp-v1.0.0-linux-amd64 serve
```

### Windows

```powershell
# Download anvilmcp-v1.0.0-windows-amd64.exe
# Run directly
.\anvilmcp-v1.0.0-windows-amd64.exe serve
```

### Docker

For releases, Docker image is automatically built and can be pushed to Docker Hub if credentials are configured.

**Configuration** (optional):
1. Add GitHub secrets:
   - `DOCKER_USERNAME` - Docker Hub username
   - `DOCKER_PASSWORD` - Docker Hub password or token

2. Once configured, releases will automatically:
   - Build Docker image
   - Push to `docker_username/anvilmcp:v1.0.0`
   - Push to `docker_username/anvilmcp:latest`

## Versioning

AnvilMCP uses **Semantic Versioning**:

- `v1.0.0` - Major version (breaking changes)
- `v1.2.0` - Minor version (new features)
- `v1.2.3` - Patch version (bug fixes)

### Version Tag Format

Use annotated tags for releases:

```bash
# Good
git tag -a v1.0.0 -m "Release version 1.0.0 - Initial release"

# Also works but less informative
git tag v1.0.0
```

## CI/CD Status

Check the status of workflows:

1. **GitHub Actions tab** - Full workflow history
2. **Branch protection rules** - Can require PR checks to pass
3. **Status badges** - Add to README.md:

```markdown
![Tests](https://github.com/artbegolli/anvilmcp/workflows/Tests/badge.svg)
![Build and Release](https://github.com/artbegolli/anvilmcp/workflows/Build%20and%20Release/badge.svg)
```

## Troubleshooting

### Release workflow failed

1. **Check logs** in Actions tab
2. **Common issues:**
   - Tag doesn't match `v*.*.*` format
   - Tests failing on one platform
   - Docker build issues

3. **Re-run workflow:**
   - Delete the tag: `git tag -d v1.0.0`
   - Push deletion: `git push origin :refs/tags/v1.0.0`
   - Fix issues and create new tag

### Docker Hub push failed

- Check `DOCKER_USERNAME` and `DOCKER_PASSWORD` secrets are configured
- Verify credentials are valid
- Check Docker Hub account has access

### Coverage upload failed

- Codecov integration is optional
- Workflow continues even if coverage upload fails
- Check codecov.io account is connected

## Best Practices

1. **Always run tests locally before creating release:**
   ```bash
   make test
   ```

2. **Test Docker image locally:**
   ```bash
   make docker-build
   make docker-run
   ```

3. **Update CHANGELOG before release** (if maintained)

4. **Use descriptive release notes** when creating GitHub release

5. **Tag when code is on main:**
   - Only release from main branch
   - Ensure all PR reviews are complete

6. **Verify binaries work:**
   - Download and test release binary on target platform
   - Check SHA256 checksums

## Workflow Configuration Files

- `.github/workflows/test.yml` - Continuous testing
- `.github/workflows/build-and-release.yml` - Release builds

These files are version-controlled and can be modified as needed.

## References

- [GitHub Actions Documentation](https://docs.github.com/en/actions)
- [Semantic Versioning](https://semver.org/)
- [GoReleaser](https://goreleaser.com/) - Alternative for advanced release management

---

For questions or issues with releases, check GitHub Actions logs or create an issue on the repository.
