package kernel

import "fmt"

// EnvSpec selects which interpreter runs the harness.
type EnvSpec struct {
	Type string `json:"type"` // "system" (this build); "conda"/"uv" in a later milestone
	Ref  string `json:"ref"`  // env name / project dir for conda/uv
}

// StartSpec is the request to start a kernel.
type StartSpec struct {
	Env  EnvSpec
	Name string
}

// resolveLaunch turns a StartSpec into the process argv and a human label.
// This build supports only the system interpreter; conda/uv arrive in a later
// milestone with PATH detection. The harness reads its socket from fd 3 (set
// via cmd.ExtraFiles), so "--fd 3" is always passed.
//
// "system" launches PythonPath (config PYTHON_PATH, default "python3"), and the
// kernel process inherits the server's environment (startKernel does not set
// cmd.Env). So system resolves against the server's PATH and picks up whatever
// Python environment is active for the server process: if the server runs
// inside an activated conda env or venv (CONDA_PREFIX / PATH set), the kernel
// launches in that same environment. Selecting a *different* conda env / uv
// project (independent of the server's active env) is the milestone-3 work.
func (m *Manager) resolveLaunch(spec StartSpec) (argv []string, label string, err error) {
	switch spec.Env.Type {
	case "", "system":
		return []string{m.pythonPath, m.harnessPath, "--fd", "3"}, "system", nil
	case "conda", "uv":
		return nil, "", fmt.Errorf("env type %q is not supported in this build (system only)", spec.Env.Type)
	default:
		return nil, "", fmt.Errorf("unknown env type %q", spec.Env.Type)
	}
}
