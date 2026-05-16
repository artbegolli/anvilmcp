# Build stage
FROM golang:1.22-alpine AS builder

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
    && rm -rf /var/lib/apt/lists/*

# Install micromamba
ENV MAMBA_ROOT_PREFIX=/opt/micromamba
RUN mkdir -p ${MAMBA_ROOT_PREFIX} && \
    curl -Ls https://micromamba.snakepit.net/api/micromamba/linux-64/latest | tar -xvj -C /usr/local/bin --strip-components=1 && \
    micromamba create -y -n base -c conda-forge python=3.11 && \
    micromamba clean --all -y

# Create non-root user
RUN useradd -m -s /bin/bash -u 1000 anvilmcp && \
    chown -R anvilmcp:anvilmcp ${MAMBA_ROOT_PREFIX}

# Set up working directory
WORKDIR /home/anvilmcp
RUN chown -R anvilmcp:anvilmcp /home/anvilmcp

# Switch to non-root user
USER anvilmcp

# Install Python packages in micromamba environment
RUN eval "$(micromamba shell-init -s bash)" && \
    micromamba activate base && \
    micromamba install -y -c conda-forge \
    pandas \
    numpy \
    scipy \
    scikit-learn \
    matplotlib \
    jupyter \
    ipython \
    requests \
    && micromamba clean --all -y

# Copy the binary from builder
COPY --from=builder --chown=anvilmcp:anvilmcp /app/bin/anvilmcp /home/anvilmcp/anvilmcp

# Create directories for workspace and config
RUN mkdir -p /home/anvilmcp/.config/mcp-session /home/anvilmcp/workspace

# Set environment
ENV PATH="/opt/micromamba/envs/base/bin:$PATH"
ENV ANVILMCP_WORKSPACE_ROOT=/home/anvilmcp/workspace

# Expose port for MCP server (default SSE port)
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Run the MCP server
CMD ["/home/anvilmcp/anvilmcp", "serve"]
