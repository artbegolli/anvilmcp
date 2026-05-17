# This Dockerfile exists for local/CI integration testing only. AnvilMCP is
# not distributed as a container image — production deployments run the
# binary directly inside the dev container provided by the host platform.
# See docs/INTEGRATION_TESTING.md.

# Build stage
FROM golang:1.25.5-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/bin/anvilmcp ./cmd/anvilmcp

# Runtime stage
FROM ubuntu:24.04

# Install system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    wget \
    git \
    bzip2 \
    && rm -rf /var/lib/apt/lists/*

# Install micromamba
ENV MAMBA_ROOT_PREFIX=/opt/micromamba
RUN ARCH=$(uname -m) && \
    if [ "$ARCH" = "aarch64" ]; then \
      MAMBA_ARCH="linux-aarch64"; \
    else \
      MAMBA_ARCH="linux-64"; \
    fi && \
    curl -Ls https://micromamba.snakepit.net/api/micromamba/${MAMBA_ARCH}/latest | tar -xvj -C /usr/local/bin --strip-components=1 && \
    micromamba shell init -s bash && \
    micromamba install -y -c conda-forge python=3.12 \
    pandas \
    numpy \
    scipy \
    scikit-learn \
    matplotlib \
    jupyter \
    ipython \
    requests \
    && micromamba clean --all -y

# Create non-root user
RUN useradd -m -s /bin/bash -u 1000 -o anvilmcp && \
    chown -R anvilmcp:anvilmcp ${MAMBA_ROOT_PREFIX}

# Set up working directory
WORKDIR /home/anvilmcp
RUN chown -R anvilmcp:anvilmcp /home/anvilmcp

# Copy the binary from builder
COPY --from=builder --chown=anvilmcp:anvilmcp /app/bin/anvilmcp /home/anvilmcp/anvilmcp

# Create directories for workspace and config
RUN mkdir -p /home/anvilmcp/.config/anvilmcp /home/anvilmcp/workspace

# Copy default policy file
COPY --chown=anvilmcp:anvilmcp .config/anvilmcp/config.yaml /home/anvilmcp/.config/anvilmcp/config.yaml

# Switch to non-root user
USER anvilmcp

# Set environment
ENV PATH="/opt/micromamba/bin:$PATH"
ENV ANVILMCP_WORKSPACE_ROOT=/home/anvilmcp/workspace

# Expose port for MCP server (default SSE port)
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Run the MCP server
CMD ["/home/anvilmcp/anvilmcp", "serve"]
