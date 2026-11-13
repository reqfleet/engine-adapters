//go:build integration && linux

// Copyright reqfleet contributors. Licensed under Apache-2.0.
package conformance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reqfleet/engine-adapters/k6/adapter"
	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/require"
)

const extensionPlan = `import faker from 'k6/x/faker'; export default function(){faker.person.firstName();}`
const incompatiblePlan = `"use k6 > 999.0.0"; export default function(){}`
const extensionDirectivePlan = `"use k6 with k6/x/faker >= 1.0.0"; export default function(){}`

// A functioning local builder is essential: network isolation alone would hide
// provisioning attempts behind download failures. Its executable only writes a
// marker; no third-party extension or replacement engine is fetched.
type provisioningPeer struct {
	url      string
	requests atomic.Int64
}

func newProvisioningPeer(t *testing.T) *provisioningPeer {
	t.Helper()
	require.Zero(t, os.Geteuid(), "use the container-only conformance target")
	_, err := os.Stat("/.dockerenv")
	require.NoError(t, err)
	peer := &provisioningPeer{}
	const binary = "#!/bin/sh\nprintf 'replacement executed\\n' > \"$HOME/replacement-executed\"\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer.requests.Add(1)
		switch r.URL.Path {
		case "/build":
			var request struct {
				Platform     string `json:"platform"`
				K6           string `json:"k6"`
				Dependencies []struct {
					Name        string `json:"name"`
					Constraints string `json:"constraints"`
				} `json:"dependencies"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			dependencies := map[string]string{"k6": request.K6}
			for _, dependency := range request.Dependencies {
				dependencies[dependency.Name] = dependency.Constraints
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"artifact": map[string]any{
				"id": "conformance-replacement", "url": peer.url + "/binary",
				"platform": request.Platform, "dependencies": dependencies,
				"checksum": fmt.Sprintf("%x", sha256.Sum256([]byte(binary))),
			}})
		case "/binary":
			_, _ = io.WriteString(w, binary)
		default:
			http.NotFound(w, r)
		}
	}))
	peer.url = server.URL
	t.Cleanup(server.Close)
	// Positive controls prove each fixture can provision and execute a binary
	// through this peer when the adapter's policy is absent.
	for _, source := range []string{extensionPlan, incompatiblePlan, extensionDirectivePlan} {
		home := t.TempDir()
		file := filepath.Join(home, "script.js")
		require.NoError(t, os.WriteFile(file, []byte(source), 0600))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "/usr/local/bin/k6", "inspect", file)
		cmd.Env = peer.environment(home)
		output, err := cmd.CombinedOutput()
		cancel()
		require.NoError(t, err, "%s", output)
		marker, err := os.ReadFile(filepath.Join(home, "replacement-executed"))
		require.NoError(t, err, "%s", output)
		require.Equal(t, "replacement executed\n", string(marker))
	}
	require.EqualValues(t, 6, peer.requests.Load(), "each control builds and downloads once")
	peer.requests.Store(0)
	return peer
}

func (p *provisioningPeer) environment(home string) []string {
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home,
		"K6_AUTO_EXTENSION_RESOLUTION=true", "K6_BUILD_SERVICE_URL=" + p.url}
}

func (p *provisioningPeer) adapterLauncher(t *testing.T) string {
	t.Helper()
	// The shared harness deliberately supplies a minimal environment. This
	// test-only launcher injects conflicting deployment settings without
	// changing the harness or the production executable.
	dir, err := os.MkdirTemp("/tmp", "k6-provisioning-launcher-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.Chmod(dir, 0755))
	file := filepath.Join(dir, "agent")
	source := fmt.Sprintf("#!/bin/sh\nexport K6_AUTO_EXTENSION_RESOLUTION=true\nexport K6_BUILD_SERVICE_URL=%s\nexec /usr/local/bin/k6-adapter\n", p.url)
	require.NoError(t, os.WriteFile(file, []byte(source), 0755))
	return file
}

func (p *provisioningPeer) assertUnused(t *testing.T, home string) {
	t.Helper()
	require.Zero(t, p.requests.Load(), "the pinned engine must not contact the build service or download a binary")
	require.NoFileExists(t, filepath.Join(home, "replacement-executed"))
}

// Mutation after preparation reaches the workload invocation independently of
// the inspection guard, so dropping only the run environment override fails.
func TestWorkloadRejectsProvisioning(t *testing.T) {
	peer := newProvisioningPeer(t)
	home := t.TempDir()
	file := filepath.Join(home, "script.js")
	require.NoError(t, os.WriteFile(file, []byte("export default function(){}"), 0600))
	t.Setenv("HOME", home)
	t.Setenv("K6_AUTO_EXTENSION_RESOLUTION", "true")
	t.Setenv("K6_BUILD_SERVICE_URL", peer.url)
	run := agent.RunContext{RunID: "pinned-workload", TestFile: file, ResultFile: filepath.Join(home, "k6.csv"),
		Workspace: agent.Workspace{Inputs: home, Temp: home},
		Settings:  json.RawMessage(`{"concurrency":"1","duration":"1","rampup":"0"}`)}
	a := adapter.New(provisioningReporter{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, a.BeforeStart(ctx, run))
	require.NoError(t, os.WriteFile(file, []byte(extensionPlan), 0600))
	command, err := a.BuildCommand(ctx, run)
	require.NoError(t, err)
	cmd := exec.CommandContext(ctx, command.Command, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = append(peer.environment(home), command.Env...)
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "k6/x/faker")
	peer.assertUnused(t, home)
}

type provisioningReporter struct{}

func (provisioningReporter) Reset(agent.RunContext) error { return nil }
func (provisioningReporter) Observe(context.Context, agent.RunContext) (protocol.Observation, error) {
	return protocol.ObservationPending, nil
}
