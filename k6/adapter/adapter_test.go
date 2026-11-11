package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/protocol"
	"github.com/stretchr/testify/require"
)

type reporter struct{ resets []string }

func (r *reporter) Reset(run agent.RunContext) { r.resets = append(r.resets, run.RunID) }
func (*reporter) Observe(context.Context, agent.RunContext) (protocol.Observation, error) {
	return protocol.ObservationPending, nil
}

func TestStrictSettings(t *testing.T) {
	for _, name := range []string{"concurrency", "duration", "rampup"} {
		for _, invalid := range []string{`null`, `1`, `true`, `[]`, `{}`, `""`, `"-1"`, `"+1"`, `" 1"`, `"1.0"`, `"1e2"`, `"9223372036854775808"`} {
			t.Run(name+"/"+invalid, func(t *testing.T) {
				values := map[string]json.RawMessage{"concurrency": json.RawMessage(`"2"`), "duration": json.RawMessage(`"1"`), "rampup": json.RawMessage(`"0"`)}
				values[name] = json.RawMessage(invalid)
				raw, err := json.Marshal(values)
				require.NoError(t, err)
				_, err = decodeSettings(raw)
				require.ErrorContains(t, err, name)
				delete(values, name)
				raw, err = json.Marshal(values)
				require.NoError(t, err)
				_, err = decodeSettings(raw)
				require.ErrorContains(t, err, name)
			})
		}
	}
	for _, raw := range []string{`null`, `{}`, `[]`, ``, `{"concurrency":"0","duration":"1","rampup":"0"}`, `{"concurrency":"1","duration":"0","rampup":"0"}`, `{"concurrency":"1","duration":"1","rampup":"61"}`, `{"concurrency":"9007199254740992","duration":"1","rampup":"0"}`, `{"concurrency":"1","duration":"153722868","rampup":"0"}`} {
		_, err := decodeSettings(json.RawMessage(raw))
		require.Error(t, err, raw)
	}
	for _, ramp := range []int{0, 1, 60} {
		s, err := decodeSettings(json.RawMessage(fmt.Sprintf(`{"concurrency":"3","duration":"1","rampup":"%d"}`, ramp)))
		require.NoError(t, err)
		require.EqualValues(t, 3, s.vus)
		require.EqualValues(t, 60, s.seconds)
		require.EqualValues(t, ramp, s.rampup)
	}
}

func fixture(t *testing.T) agent.RunContext {
	t.Helper()
	root := t.TempDir()
	script := filepath.Join(root, "original.js")
	require.NoError(t, os.WriteFile(script, []byte("export default function() {}\n"), 0600))
	return agent.RunContext{RunID: "run-1", TestFile: script, ResultFile: filepath.Join(root, "result.csv"), Workspace: agent.Workspace{Inputs: root, Temp: root}, Settings: json.RawMessage(`{"concurrency":"7","duration":"1","rampup":"0"}`)}
}

func TestPreparationAndCommand(t *testing.T) {
	run := fixture(t)
	r := &reporter{}
	a := New(r)
	a.executable = filepath.Join(t.TempDir(), "k6")
	require.NoError(t, os.WriteFile(a.executable, []byte("#!/bin/sh\nprintf '{\"customBuildRequired\":false}\\n'\n"), 0700))
	_, err := a.BuildCommand(context.Background(), run)
	require.Error(t, err)
	run.Artifacts = map[string]protocol.ArtifactAssignment{"copy.csv": {Mode: protocol.PartitionReplicated}, "split.csv": {Mode: protocol.PartitionMaterialized}}
	require.NoError(t, a.BeforeStart(context.Background(), run))
	original, err := os.ReadFile(run.TestFile)
	require.NoError(t, err)
	require.Equal(t, "export default function() {}\n", string(original))
	_, err = a.BuildCommand(context.Background(), run)
	require.NoError(t, err)
	run.RunID = "run-2"
	_, err = a.BuildCommand(context.Background(), run)
	require.Error(t, err)
	run.Settings = json.RawMessage(`{}`)
	require.Error(t, a.BeforeStart(context.Background(), run))
	require.Equal(t, []string{"run-1", "run-2"}, r.resets)
	run.RunID = "run-1"
	_, err = a.BuildCommand(context.Background(), run)
	require.Error(t, err, "failed preparation invalidates old prepared ID")
}

func TestCancellationAndAssignmentFailures(t *testing.T) {
	run := fixture(t)
	r := &reporter{}
	a := New(r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, a.BeforeStart(ctx, run), context.Canceled)
	require.Equal(t, []string{run.RunID}, r.resets)
	require.NoFileExists(t, filepath.Join(run.Workspace.Temp, "entry.js"))
	for _, mode := range []protocol.PartitionMode{protocol.PartitionNative, "", "unknown"} {
		run.Artifacts = map[string]protocol.ArtifactAssignment{"data.csv": {Mode: mode}}
		require.ErrorContains(t, a.BeforeStart(context.Background(), run), "partition mode")
	}
	run.Artifacts = nil
	run.TestFile = filepath.Join(run.Workspace.Inputs, "missing.js")
	require.ErrorContains(t, a.BeforeStart(context.Background(), run), "test file")
	require.ErrorIs(t, writeAtomic(ctx, filepath.Join(run.Workspace.Temp, "entry.js"), "data"), context.Canceled)
}

func TestInspectionCancellation(t *testing.T) {
	run := fixture(t)
	a := New(&reporter{})
	a.executable = filepath.Join(t.TempDir(), "k6")
	require.NoError(t, os.WriteFile(a.executable, []byte("#!/bin/sh\nexec sleep 30\n"), 0700))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, a.BeforeStart(ctx, run), context.DeadlineExceeded)
	_, err := a.BuildCommand(context.Background(), run)
	require.ErrorContains(t, err, "has not been prepared")
}

func TestInspectionStderrBoundaries(t *testing.T) {
	for _, size := range []int{0, inspectionStderrLimit - 1, inspectionStderrLimit, inspectionStderrLimit + 1, 3 * inspectionStderrLimit} {
		for _, chunk := range []int{1, 137, inspectionStderrLimit, 2 * inspectionStderrLimit} {
			t.Run(fmt.Sprintf("size=%d/chunk=%d", size, chunk), func(t *testing.T) {
				input := strings.Repeat("0123456789", size/10+1)[:size]
				var output inspectionStderr
				for offset := 0; offset < len(input); offset += chunk {
					part := input[offset:min(offset+chunk, len(input))]
					n, err := output.Write([]byte(part))
					require.NoError(t, err)
					require.Equal(t, len(part), n, "discarded bytes must still be consumed")
				}
				want := input[max(0, len(input)-inspectionStderrLimit):]
				require.Equal(t, want, string(output.data[:output.used]))
				require.Equal(t, size > inspectionStderrLimit, output.truncated)
			})
		}
	}
}
