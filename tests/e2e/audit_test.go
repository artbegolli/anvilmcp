package e2e

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// auditRecord mirrors the `anvilmcp history --json` shape.
type auditRecord struct {
	ID           string         `json:"id"`
	Tool         string         `json:"tool"`
	Status       string         `json:"status"`
	Decision     string         `json:"decision"`
	PolicyRule   string         `json:"policy_rule"`
	ExitCode     *int           `json:"exit_code"`
	ResolvedPath string         `json:"resolved_path"`
	Args         map[string]any `json:"args"`
	StdoutBytes  int64          `json:"stdout_bytes"`
}

// history runs the CLI against this server's trail. Using the binary rather
// than the library is the point: it proves `anvilmcp history` reads what
// `anvilmcp serve` writes, including the path resolution between them.
func history(t *testing.T, s *testServer, args ...string) []auditRecord {
	t.Helper()
	full := append([]string{
		"history", "list", "--json",
		"--history-db", filepath.Join(s.dir, "history.db"),
		"--policy", filepath.Join(s.dir, "no-such-policy.yaml"),
		"--cluster-policy", filepath.Join(s.dir, "no-cluster-policy.yaml"),
		"--limit", "200",
	}, args...)

	out, err := exec.Command(binPath, full...).CombinedOutput()
	if err != nil {
		t.Fatalf("anvilmcp %s: %v\n%s", strings.Join(full, " "), err, out)
	}
	var records []auditRecord
	if err := json.Unmarshal(out, &records); err != nil {
		t.Fatalf("history output is not JSON: %v\n%s", err, out)
	}
	return records
}

func findRecord(records []auditRecord, tool string, match func(auditRecord) bool) (auditRecord, bool) {
	for _, r := range records {
		if r.Tool == tool && match(r) {
			return r, true
		}
	}
	return auditRecord{}, false
}

func TestAuditTrailRecordsCallsAndDecisions(t *testing.T) {
	// A dedicated server so the trail holds only this test's calls.
	s := startServer(t, serverOpts{})
	c := newClient(t, s)

	path := s.path("audited.txt")
	c.mustCallTool(t, "fs.write", map[string]any{"path": path, "content": "hello", "create": true})
	c.mustCallTool(t, "exec.shell", map[string]any{"cmd": "cat", "argv": []string{path}})

	// Two denials of different kinds.
	if tr := c.callTool(t, "exec.shell", map[string]any{"cmd": "rm", "argv": []string{"-rf", s.workspace}}, nil); !tr.IsError {
		t.Fatal("rm should have been denied")
	}
	if tr := c.callTool(t, "fs.read", map[string]any{"path": "/etc/passwd"}, nil); !tr.IsError {
		t.Fatal("/etc/passwd should have been denied")
	}

	records := history(t, s)
	if len(records) < 4 {
		t.Fatalf("recorded %d calls, want at least 4: %+v", len(records), records)
	}

	t.Run("allowed exec carries its rule and exit code", func(t *testing.T) {
		rec, ok := findRecord(records, "exec.shell", func(r auditRecord) bool {
			return r.Args["cmd"] == "cat"
		})
		if !ok {
			t.Fatal("no record for the allowed `cat` call")
		}
		if rec.Decision != "allow" {
			t.Errorf("decision = %q, want allow", rec.Decision)
		}
		if rec.Status != "ok" {
			t.Errorf("status = %q, want ok", rec.Status)
		}
		if rec.ExitCode == nil || *rec.ExitCode != 0 {
			t.Errorf("exit_code = %v, want 0", rec.ExitCode)
		}
		if rec.PolicyRule != "exec.allow:cat" {
			t.Errorf("policy_rule = %q, want exec.allow:cat", rec.PolicyRule)
		}
		if rec.StdoutBytes == 0 {
			t.Error("stdout_bytes = 0, want the output volume recorded")
		}
	})

	t.Run("denied exec is stored as denied, not as an error", func(t *testing.T) {
		rec, ok := findRecord(records, "exec.shell", func(r auditRecord) bool {
			return r.Args["cmd"] == "rm"
		})
		if !ok {
			t.Fatal("no record for the denied `rm` call")
		}
		if rec.Decision != "deny" {
			t.Errorf("decision = %q, want deny", rec.Decision)
		}
		// The distinction that makes the trail useful: a refusal must not look
		// like a crash.
		if rec.Status != "denied" {
			t.Errorf("status = %q, want denied", rec.Status)
		}
		if !strings.HasPrefix(rec.PolicyRule, "exec.deny:") {
			t.Errorf("policy_rule = %q, want an exec.deny rule", rec.PolicyRule)
		}
	})

	t.Run("denied path read names the rule that refused it", func(t *testing.T) {
		rec, ok := findRecord(records, "fs.read", func(r auditRecord) bool {
			return r.Args["path"] == "/etc/passwd"
		})
		if !ok {
			t.Fatal("no record for the denied /etc/passwd read")
		}
		if rec.Decision != "deny" || rec.Status != "denied" {
			t.Errorf("decision/status = %q/%q, want deny/denied", rec.Decision, rec.Status)
		}
		if !strings.Contains(rec.PolicyRule, "deny") {
			t.Errorf("policy_rule = %q, want a deny rule", rec.PolicyRule)
		}
	})

	t.Run("filesystem writes record the resolved target", func(t *testing.T) {
		rec, ok := findRecord(records, "fs.write", func(r auditRecord) bool { return true })
		if !ok {
			t.Fatal("no record for fs.write")
		}
		if rec.ResolvedPath == "" {
			t.Error("resolved_path is empty; symlink escapes are detected by comparing it to the raw argument")
		}
	})
}

func TestAuditTrailFiltersAndRedacts(t *testing.T) {
	s := startServer(t, serverOpts{})
	c := newClient(t, s)

	c.mustCallTool(t, "exec.shell", map[string]any{
		"cmd": "cat", "argv": []string{s.path("missing-is-fine.txt")},
		"env": map[string]string{"MY_API_KEY": "sk-should-not-be-stored", "LANG": "C"},
	})
	if tr := c.callTool(t, "exec.shell", map[string]any{"cmd": "rm", "argv": []string{"/tmp/x"}}, nil); !tr.IsError {
		t.Fatal("rm should have been denied")
	}

	t.Run("--denied returns only refusals", func(t *testing.T) {
		denied := history(t, s, "--denied")
		if len(denied) == 0 {
			t.Fatal("--denied returned nothing")
		}
		for _, r := range denied {
			if r.Status != "denied" {
				t.Errorf("--denied returned a %q record for %s", r.Status, r.Tool)
			}
		}
	})

	t.Run("--tool narrows to one tool", func(t *testing.T) {
		for _, r := range history(t, s, "--tool", "exec.shell") {
			if r.Tool != "exec.shell" {
				t.Errorf("--tool exec.shell returned %q", r.Tool)
			}
		}
		if got := history(t, s, "--tool", "fs.stat"); len(got) != 0 {
			t.Errorf("--tool fs.stat returned %d records, want 0", len(got))
		}
	})

	t.Run("secret-looking env keys are redacted on disk", func(t *testing.T) {
		raw, err := exec.Command(binPath, "history", "list", "--json",
			"--history-db", filepath.Join(s.dir, "history.db"),
			"--policy", filepath.Join(s.dir, "no-such-policy.yaml"),
			"--cluster-policy", filepath.Join(s.dir, "no-cluster-policy.yaml"),
		).CombinedOutput()
		if err != nil {
			t.Fatalf("history list: %v\n%s", err, raw)
		}
		if strings.Contains(string(raw), "sk-should-not-be-stored") {
			t.Error("the stored arguments leaked a secret-looking env value")
		}
		if !strings.Contains(string(raw), "REDACTED") {
			t.Error("expected a redaction marker in the stored arguments")
		}
		// A non-secret key in the same map must survive, or redaction is just
		// dropping the field.
		if !strings.Contains(string(raw), "LANG") {
			t.Error("redaction removed a non-secret env key")
		}
	})
}

func TestAuditTrailRecordsKernelAsUnmediated(t *testing.T) {
	s := startServer(t, serverOpts{enableKernel: true})
	c := newClient(t, s)

	var start struct {
		KernelID string `json:"kernel_id"`
	}
	c.mustCallTool(t, "kernel.start", map[string]any{}).decode(t, &start)
	if start.KernelID == "" {
		t.Fatal("kernel.start returned no kernel_id")
	}
	c.mustCallTool(t, "kernel.exec", map[string]any{
		"kernel_id": start.KernelID, "code": "secret_marker = 41 + 1",
	})

	records := history(t, s)
	rec, ok := findRecord(records, "kernel.exec", func(r auditRecord) bool { return true })
	if !ok {
		t.Fatal("kernel.exec was not recorded")
	}
	// Only the interpreter launch is policy-gated; the code is not. The trail
	// has to say so rather than implying the call was approved.
	if rec.Decision != "unmediated" {
		t.Errorf("decision = %q, want unmediated", rec.Decision)
	}
	// The code itself is the evidence; it must be stored.
	code, _ := rec.Args["code"].(string)
	if !strings.Contains(code, "secret_marker") {
		t.Errorf("args.code = %q, want the executed code recorded", code)
	}
}
