// Copyright reqfleet contributors. Licensed under Apache-2.0.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/reqfleet/engine-sdk/agent"
	"github.com/reqfleet/engine-sdk/protocol"
)

// Reporter is reset before preparation, including preparation that fails.
// SDK callbacks are serialized; the reporter synchronizes concurrent scrapes.
type Reporter interface {
	Reset(agent.RunContext)
	Observe(context.Context, agent.RunContext) (protocol.Observation, error)
}

type Adapter struct {
	reporter    Reporter
	preparedRun string
	entry       string
	executable  string
}

func New(reporter Reporter) *Adapter {
	return &Adapter{reporter: reporter, executable: "/usr/local/bin/k6"}
}

// Options adds k6 callbacks and metrics configuration to resolved SDK options.
func (a *Adapter) Options(options agent.AgentServerOptions) agent.AgentServerOptions {
	options.TestFileName = "script.js"
	options.ResultFile = "k6.csv"
	options.BeforeStart = a.BeforeStart
	options.BuildCommand = a.BuildCommand
	options.Observe = a.reporter.Observe
	options.NativeMetricsSchema = agent.MetricsSchemaShibuyaV1
	return options
}

type settings struct{ vus, seconds, rampup int64 }

func decimal(value string) (int64, error) {
	if value == "" {
		return 0, fmt.Errorf("expected a decimal integer")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("expected a decimal integer")
		}
	}
	return strconv.ParseInt(value, 10, 64)
}

func decodeSettings(raw json.RawMessage) (settings, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return settings{}, fmt.Errorf("settings: %w", err)
	}
	read := func(name string, positive bool, max int64) (int64, error) {
		var text string
		if len(values[name]) == 0 || string(values[name]) == "null" {
			return 0, fmt.Errorf("%s must be a string-valued integer", name)
		}
		if err := json.Unmarshal(values[name], &text); err != nil {
			return 0, fmt.Errorf("%s must be a string-valued integer", name)
		}
		n, err := decimal(text)
		if err != nil || n > max || (positive && n == 0) {
			return 0, fmt.Errorf("invalid %s: expected %sinteger no greater than %d", name, map[bool]string{true: "positive ", false: "nonnegative "}[positive], max)
		}
		return n, nil
	}
	vus, err := read("concurrency", true, 1<<53-1)
	if err != nil {
		return settings{}, err
	}
	minutes, err := read("duration", true, math.MaxInt64/int64(time.Minute))
	if err != nil {
		return settings{}, err
	}
	rampup, err := read("rampup", false, minutes*60)
	if err != nil {
		return settings{}, err
	}
	return settings{vus: vus, seconds: minutes * 60, rampup: rampup}, nil
}

func (a *Adapter) BeforeStart(ctx context.Context, run agent.RunContext) error {
	a.preparedRun, a.entry = "", ""
	a.reporter.Reset(run)
	if err := ctx.Err(); err != nil {
		return err
	}
	if run.RunID == "" {
		return fmt.Errorf("run ID is missing")
	}
	s, err := decodeSettings(run.Settings)
	if err != nil {
		return err
	}
	for filename, assignment := range run.Artifacts {
		switch assignment.Mode {
		case protocol.PartitionReplicated, protocol.PartitionMaterialized:
			// Consume exactly the bytes provided by the coordinator. No re-sharding.
		default:
			return fmt.Errorf("artifact %q: k6 does not support partition mode %q", filename, assignment.Mode)
		}
	}
	info, err := os.Stat(run.TestFile)
	if err != nil {
		return fmt.Errorf("test file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("test file must be a regular file")
	}
	source, err := filepath.Abs(run.TestFile)
	if err != nil {
		return err
	}
	encodedPath, err := json.Marshal(source)
	if err != nil {
		return err
	}
	schedule := map[string]any{"executor": "constant-vus", "vus": s.vus, "duration": fmt.Sprintf("%ds", s.seconds), "gracefulStop": "0s"}
	if s.rampup > 0 {
		stages := []map[string]any{{"duration": fmt.Sprintf("%ds", s.rampup), "target": s.vus}}
		if s.rampup < s.seconds {
			stages = append(stages, map[string]any{"duration": fmt.Sprintf("%ds", s.seconds-s.rampup), "target": s.vus})
		}
		schedule = map[string]any{"executor": "ramping-vus", "startVUs": 0, "stages": stages, "gracefulRampDown": "0s", "gracefulStop": "0s"}
	}
	encodedSchedule, err := json.Marshal(schedule)
	if err != nil {
		return err
	}
	entry := filepath.Join(run.Workspace.Temp, "entry.js")
	content := fmt.Sprintf(wrapper, encodedPath, encodedSchedule)
	if err := writeAtomic(ctx, entry, content); err != nil {
		return fmt.Errorf("prepare k6 wrapper: %w", err)
	}
	if err := a.check(ctx, run, nil, "inspect", entry); err != nil {
		return err
	}
	// With resolution disabled, inspect accepts unsatisfied "use k6"
	// constraints. Ask the pinned engine to check them; never parse JS here.
	var output bytes.Buffer
	if err := a.check(ctx, run, &output, "deps", "--json", entry); err != nil {
		return err
	}
	var dependencies struct {
		CustomBuildRequired *bool             `json:"customBuildRequired"`
		BuildDependencies   map[string]string `json:"buildDependencies"`
	}
	if err := json.Unmarshal(output.Bytes(), &dependencies); err != nil {
		return fmt.Errorf("k6 dependency check: %w", err)
	}
	if dependencies.CustomBuildRequired == nil {
		return fmt.Errorf("k6 dependency check: missing customBuildRequired")
	}
	if *dependencies.CustomBuildRequired {
		return fmt.Errorf("pinned k6 binary does not satisfy plan dependencies %v; custom binaries and automatic extension resolution are disabled", dependencies.BuildDependencies)
	}
	a.preparedRun, a.entry = run.RunID, entry
	return nil
}

func (a *Adapter) BuildCommand(ctx context.Context, run agent.RunContext) (agent.Command, error) {
	if err := ctx.Err(); err != nil {
		return agent.Command{}, err
	}
	if run.RunID == "" || a.preparedRun != run.RunID {
		return agent.Command{}, fmt.Errorf("run %q has not been prepared", run.RunID)
	}
	return agent.Command{Command: a.executable, Args: []string{"run", "--no-usage-report", "--out", "csv=" + run.ResultFile, a.entry}, Dir: run.Workspace.Inputs, Env: engineEnv()}, nil
}

func engineEnv() []string {
	return []string{"K6_CSV_SAVE_INTERVAL=1s", "K6_AUTO_EXTENSION_RESOLUTION=false"}
}

func (a *Adapter) check(ctx context.Context, run agent.RunContext, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, a.executable, args...)
	cmd.Dir = run.Workspace.Inputs
	cmd.Env = append(os.Environ(), engineEnv()...)
	cmd.Stdout = stdout
	var stderr inspectionStderr
	cmd.Stderr = &stderr
	err := cmd.Run()
	// CommandContext can return "signal: killed" on cancellation; preserve the
	// context error rather than reporting it as a plan validation failure.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		detail := strings.TrimSpace(string(stderr.data[:stderr.used]))
		if stderr.truncated {
			detail = "[stderr truncated; showing final 16 KiB]\n" + detail
		}
		return fmt.Errorf("k6 %s failed: %w: %s", args[0], err, detail)
	}
	return nil
}

const inspectionStderrLimit = 16 * 1024

// inspectionStderr retains the tail while draining all output. Init logging
// precedes k6's final error diagnostic, so retaining the head could lose the
// failure reason after verbose logs.
type inspectionStderr struct {
	data      [inspectionStderrLimit]byte
	used      int // Number of valid bytes retained in data[:used]
	truncated bool
}

func (b *inspectionStderr) Write(p []byte) (int, error) {
	n := len(p)
	if n >= len(b.data) {
		b.truncated = b.truncated || b.used > 0 || n > len(b.data)
		b.used = copy(b.data[:], p[n-len(b.data):])
		return n, nil
	}
	if overflow := b.used + n - len(b.data); overflow > 0 {
		copy(b.data[:], b.data[overflow:b.used])
		b.used -= overflow
		b.truncated = true
	}
	b.used += copy(b.data[b.used:], p)
	return n, nil
}

func writeAtomic(ctx context.Context, destination, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".entry-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), destination); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return nil
}

// Namespace import permits setup/teardown to be absent without static import errors.
// All validation runs inside k6, not in the API or a second JS interpreter.
const wrapper = `import * as plan from %s;
if (typeof plan.default !== "function") {
  throw new Error("k6 plan must export a default function");
}
const originalOptions = plan.options || {};
for (const key of ["scenarios", "vus", "duration", "iterations", "stages", "executionSegment", "executionSegmentSequence"]) {
  if (Object.prototype.hasOwnProperty.call(originalOptions, key)) {
    throw new Error("k6 scheduling options are managed by the collection");
  }
}
const retainedOptions = Object.assign({}, originalOptions);
if (Object.prototype.hasOwnProperty.call(originalOptions, "systemTags")) {
  if (!Array.isArray(originalOptions.systemTags)) {
    throw new Error("k6 options.systemTags must be an array");
  }
  retainedOptions.systemTags = originalOptions.systemTags.slice();
  if (retainedOptions.systemTags.indexOf("status") === -1) retainedOptions.systemTags.push("status");
  if (retainedOptions.systemTags.indexOf("url") === -1) retainedOptions.systemTags.push("url");
  if (retainedOptions.systemTags.indexOf("error_code") === -1) retainedOptions.systemTags.push("error_code");
}
export const options = Object.assign(retainedOptions, {scenarios: {reqfleet: %s}});
export default function(data) { return plan.default(data); }
export function setup() { if (typeof plan.setup === "function") return plan.setup(); }
export function teardown(data) { if (typeof plan.teardown === "function") return plan.teardown(data); }
`
