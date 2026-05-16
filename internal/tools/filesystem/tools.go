package filesystem

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/artbegolli/anvilmcp/internal/policy"
	"go.uber.org/zap"
)

// Tools implements filesystem MCP tools.
type Tools struct {
	engine *policy.Engine
	logger *zap.Logger
}

// NewTools creates a new filesystem tools handler.
func NewTools(engine *policy.Engine, logger *zap.Logger) *Tools {
	return &Tools{
		engine: engine,
		logger: logger,
	}
}

// ListRequest is the schema for fs.list.
type ListRequest struct {
	Path      string `json:"path"`
	Recursive *bool  `json:"recursive,omitempty"`
	Glob      *string `json:"glob,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

// ListEntry represents a file/directory in the listing.
type ListEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // file, dir, symlink
	Size  int64  `json:"size,omitempty"`
	MTime int64  `json:"mtime,omitempty"`
}

// ListResponse is the response from fs.list.
type ListResponse struct {
	Entries []ListEntry `json:"entries"`
}

// HandleList handles the fs.list tool.
func (t *Tools) HandleList(ctx context.Context, request *ListRequest) (*ListResponse, error) {
	// Resolve path
	decision := t.engine.ResolvePath(request.Path, "read")
	if !decision.Allowed {
		return nil, fmt.Errorf("access denied: %s (rule: %s)", decision.Reason, decision.PolicyRule)
	}

	absPath := decision.AbsPath

	// Check if directory
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat %s: %w", absPath, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", absPath)
	}

	var entries []ListEntry
	limit := 1000
	if request.Limit != nil && *request.Limit > 0 {
		// SECURITY FIX #6b: Validate limit doesn't allow excessive memory usage
		if *request.Limit > 100000 {
			return nil, fmt.Errorf("limit too large: maximum 100000 entries allowed, requested %d", *request.Limit)
		}
		limit = *request.Limit
	}

	if request.Recursive != nil && *request.Recursive {
		// SECURITY FIX #6b: Add timeout for recursive walks (prevent DoS)
		walkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		entries, err = t.walkDir(walkCtx, absPath, request.Glob, limit)
	} else {
		entries, err = t.listDir(absPath, request.Glob, limit)
	}

	if err != nil {
		return nil, err
	}

	return &ListResponse{Entries: entries}, nil
}

// ReadRequest is the schema for fs.read.
type ReadRequest struct {
	Path      string `json:"path"`
	Offset    *int64 `json:"offset,omitempty"`
	Length    *int64 `json:"length,omitempty"`
	Encoding  *string `json:"encoding,omitempty"`
}

// ReadResponse is the response from fs.read.
type ReadResponse struct {
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Size      int64  `json:"size"`
	MTime     int64  `json:"mtime"`
}

// HandleRead handles the fs.read tool.
func (t *Tools) HandleRead(ctx context.Context, request *ReadRequest) (*ReadResponse, error) {
	// Resolve path
	decision := t.engine.ResolvePath(request.Path, "read")
	if !decision.Allowed {
		return nil, fmt.Errorf("access denied: %s (rule: %s)", decision.Reason, decision.PolicyRule)
	}

	absPath := decision.AbsPath

	// Check if file
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat %s: %w", absPath, err)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", absPath)
	}

	// Open and read
	file, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", absPath, err)
	}
	defer file.Close()

	// Apply offset if provided
	if request.Offset != nil && *request.Offset > 0 {
		if _, err := file.Seek(*request.Offset, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to seek: %w", err)
		}
	}

	// Limit to reasonable size
	maxRead := int64(10 * 1024 * 1024) // 10MB max
	if request.Length != nil && *request.Length > 0 && *request.Length < maxRead {
		maxRead = *request.Length
	}

	buf := make([]byte, maxRead)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to read: %w", err)
	}

	truncated := n == int(maxRead) && err != io.EOF

	return &ReadResponse{
		Content:   string(buf[:n]),
		Truncated: truncated,
		Size:      info.Size(),
		MTime:     info.ModTime().UnixMilli(),
	}, nil
}

// WriteRequest is the schema for fs.write.
type WriteRequest struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Mode      *int   `json:"mode,omitempty"`
	Create    *bool  `json:"create,omitempty"`
	Overwrite *bool  `json:"overwrite,omitempty"`
}

// WriteResponse is the response from fs.write.
type WriteResponse struct {
	BytesWritten int64 `json:"bytes_written"`
	MTime        int64 `json:"mtime"`
}

// HandleWrite handles the fs.write tool.
func (t *Tools) HandleWrite(ctx context.Context, request *WriteRequest) (*WriteResponse, error) {
	// Resolve path
	decision := t.engine.ResolvePath(request.Path, "write")
	if !decision.Allowed {
		return nil, fmt.Errorf("access denied: %s (rule: %s)", decision.Reason, decision.PolicyRule)
	}

	absPath := decision.AbsPath

	// Check if file exists
	_, err := os.Stat(absPath)
	exists := err == nil

	if exists {
		overwrite := request.Overwrite != nil && *request.Overwrite
		if !overwrite {
			return nil, fmt.Errorf("file exists and overwrite is false")
		}
	} else {
		create := request.Create == nil || *request.Create
		if !create {
			return nil, fmt.Errorf("file does not exist and create is false")
		}

		// SECURITY FIX #1: Validate parent directory path against policy before creation
		dir := filepath.Dir(absPath)
		dirDecision := t.engine.ResolvePath(dir, "write")
		if !dirDecision.Allowed {
			return nil, fmt.Errorf("parent directory access denied: operation not permitted (rule: %s)", dirDecision.PolicyRule)
		}

		// Create directory with validated path
		if err := os.MkdirAll(dirDecision.AbsPath, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}
	}

	// Write file
	mode := os.FileMode(0644)
	if request.Mode != nil {
		mode = os.FileMode(*request.Mode)
	}

	if err := os.WriteFile(absPath, []byte(request.Content), mode); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	info, _ := os.Stat(absPath)

	return &WriteResponse{
		BytesWritten: int64(len(request.Content)),
		MTime:        info.ModTime().UnixMilli(),
	}, nil
}

// StatRequest is the schema for fs.stat.
type StatRequest struct {
	Path string `json:"path"`
}

// StatResponse is the response from fs.stat.
type StatResponse struct {
	Type        string `json:"type"` // file, dir, symlink, other
	Size        int64  `json:"size"`
	MTime       int64  `json:"mtime"`
	Permissions string `json:"permissions"` // e.g., "0644"
}

// HandleStat handles the fs.stat tool.
func (t *Tools) HandleStat(ctx context.Context, request *StatRequest) (*StatResponse, error) {
	// Resolve path
	decision := t.engine.ResolvePath(request.Path, "read")
	if !decision.Allowed {
		return nil, fmt.Errorf("access denied: %s (rule: %s)", decision.Reason, decision.PolicyRule)
	}

	absPath := decision.AbsPath

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat: %w", err)
	}

	typ := "file"
	if info.IsDir() {
		typ = "dir"
	}

	return &StatResponse{
		Type:        typ,
		Size:        info.Size(),
		MTime:       info.ModTime().UnixMilli(),
		Permissions: fmt.Sprintf("0%o", info.Mode().Perm()),
	}, nil
}

// SearchRequest is the schema for fs.search.
type SearchRequest struct {
	Pattern    string `json:"pattern"`
	Path       *string `json:"path,omitempty"`
	Glob       *string `json:"glob,omitempty"`
	MaxResults *int   `json:"max_results,omitempty"`
}

// SearchResult is a single search result.
type SearchResult struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Text string `json:"text"`
}

// HandleSearch handles the fs.search tool (streaming).
func (t *Tools) HandleSearch(ctx context.Context, request *SearchRequest) (interface{}, error) {
	if request.Path != nil {
		decision := t.engine.ResolvePath(*request.Path, "read")
		if !decision.Allowed {
			return nil, fmt.Errorf("access denied: %s (rule: %s)", decision.Reason, decision.PolicyRule)
		}
		_ = decision // TODO: use for actual search
	}

	// For now, return a simple text result (Phase 1)
	// TODO: implement full grep-like search with rg integration
	result := map[string]interface{}{
		"results": []SearchResult{},
		"note":    "full search implementation pending",
	}

	return result, nil
}

// DeleteRequest is the schema for fs.delete.
type DeleteRequest struct {
	Path      string `json:"path"`
	Recursive *bool  `json:"recursive,omitempty"`
}

// DeleteResponse is the response from fs.delete.
type DeleteResponse struct {
	Deleted int `json:"deleted"`
}

// HandleDelete handles the fs.delete tool.
func (t *Tools) HandleDelete(ctx context.Context, request *DeleteRequest) (*DeleteResponse, error) {
	// Resolve path
	decision := t.engine.ResolvePath(request.Path, "write")
	if !decision.Allowed {
		return nil, fmt.Errorf("access denied: %s (rule: %s)", decision.Reason, decision.PolicyRule)
	}

	absPath := decision.AbsPath

	// Check existence
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("path does not exist: %w", err)
	}

	deleted := 1
	if info.IsDir() {
		recursive := request.Recursive != nil && *request.Recursive
		if !recursive {
			return nil, fmt.Errorf("directory deletion requires recursive=true")
		}
		if err := os.RemoveAll(absPath); err != nil {
			return nil, fmt.Errorf("failed to remove directory: %w", err)
		}
	} else {
		if err := os.Remove(absPath); err != nil {
			return nil, fmt.Errorf("failed to remove file: %w", err)
		}
	}

	return &DeleteResponse{Deleted: deleted}, nil
}

// Helper functions

func (t *Tools) listDir(path string, glob *string, limit int) ([]ListEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	var results []ListEntry
	for i, entry := range entries {
		if i >= limit {
			break
		}

		if glob != nil {
			match, _ := filepath.Match(*glob, entry.Name())
			if !match {
				continue
			}
		}

		info, _ := entry.Info()
		typ := "file"
		if entry.IsDir() {
			typ = "dir"
		} else if entry.Type()&os.ModeSymlink != 0 {
			typ = "symlink"
		}

		results = append(results, ListEntry{
			Name:  entry.Name(),
			Type:  typ,
			Size:  info.Size(),
			MTime: info.ModTime().UnixMilli(),
		})
	}

	return results, nil
}

func (t *Tools) walkDir(ctx context.Context, path string, glob *string, limit int) ([]ListEntry, error) {
	var results []ListEntry

	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		// SECURITY FIX #6b: Check if context cancelled (timeout or cancellation)
		select {
		case <-ctx.Done():
			return ctx.Err() // Stop walk immediately on timeout
		default:
		}

		if err != nil {
			return err
		}

		if len(results) >= limit {
			return filepath.SkipDir
		}

		if glob != nil {
			match, _ := filepath.Match(*glob, d.Name())
			if !match {
				return nil
			}
		}

		info, _ := d.Info()
		typ := "file"
		if d.IsDir() {
			typ = "dir"
		} else if d.Type()&os.ModeSymlink != 0 {
			typ = "symlink"
		}

		results = append(results, ListEntry{
			Name:  p,
			Type:  typ,
			Size:  info.Size(),
			MTime: info.ModTime().UnixMilli(),
		})

		return nil
	})

	return results, err
}

// RegisterTools registers filesystem tools with the MCP server.
// Note: Full schema registration deferred — handlers validate inputs directly.
func RegisterTools(srv interface{}, tools *Tools) error {
	return nil // TODO: wire into MCP server during Phase 1 integration
}
