package metrics

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reqfleet/engine-sdk/metricschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cgroupFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0700))
	require.NoError(t, os.WriteFile(full, []byte(content), 0600))
}

func TestCgroupVersionsProduceIdenticalCPUUnitsAndRates(t *testing.T) {
	for _, version := range []string{"v2", "v1-combined", "v1-separate"} {
		t.Run(version, func(t *testing.T) {
			root := t.TempDir()
			cpuPath, memoryPath := "cpu,cpuacct/cpuacct.usage", "memory/memory.usage_in_bytes"
			if version == "v2" {
				cgroupFile(t, root, "cgroup.controllers", "cpu memory\n")
				cpuPath, memoryPath = "cpu.stat", "memory.current"
			} else if version == "v1-separate" {
				cpuPath = "cpuacct/cpuacct.usage"
			}
			setCPU := func(nanos uint64) {
				value := fmt.Sprintf("%d\n", nanos)
				if version == "v2" {
					value = fmt.Sprintf("nr_periods 0\nusage_usec %d\nuser_usec 1\nsystem_usec 2\n", nanos/1000)
				}
				cgroupFile(t, root, cpuPath, value)
			}
			setCPU(2_000_000_000)
			cgroupFile(t, root, memoryPath, "123456\n")
			reader := cgroupReader(root)
			sample, err := reader()
			require.NoError(t, err)
			assert.Equal(t, uint64(2_000_000_000), sample.cpuNanos)
			assert.Equal(t, uint64(123456), sample.memoryBytes)
			e, run := fixture(t, 10)
			putCSV(t, run, csvHeader+"vus,0,\n")
			e.resources = reader
			clock := time.Unix(100, 0)
			e.now = func() time.Time { return clock }
			assert.NotContains(t, families(t, e), metricschema.CPUGauge)
			setCPU(3_500_000_000)
			clock = clock.Add(2 * time.Second)
			assert.Equal(t, 0.75, single(t, families(t, e), metricschema.CPUGauge).GetGauge().GetValue())
			clock = clock.Add(time.Second)
			assert.Equal(t, float64(0), single(t, families(t, e), metricschema.CPUGauge).GetGauge().GetValue(), "unchanged actual usage is a real zero")
		})
	}
}

func TestCgroupInvalidAndUnavailableResources(t *testing.T) {
	cases := []struct {
		name   string
		v2     bool
		cpu    string
		memory string
	}{
		{"v1-missing", false, "", ""},
		{"v1-negative", false, "-1\n", "10\n"},
		{"v1-fraction", false, "1.5\n", "10\n"},
		{"v1-overflow", false, "18446744073709551616\n", "10\n"},
		{"v1-extra-lines", false, "1\n2\n", "10\n"},
		{"v1-memory-missing", false, "1000\n", ""},
		{"v1-memory-invalid", false, "1000\n", "oops\n"},
		{"v2-missing", true, "", "10\n"},
		{"v2-missing-key", true, "user_usec 100\n", "10\n"},
		{"v2-negative", true, "usage_usec -1\n", "10\n"},
		{"v2-plus", true, "usage_usec +1\n", "10\n"},
		{"v2-fraction", true, "usage_usec 1.5\n", "10\n"},
		{"v2-missing-value", true, "usage_usec\n", "10\n"},
		{"v2-extra-value", true, "usage_usec 1 2\n", "10\n"},
		{"v2-duplicate", true, "usage_usec 1\nusage_usec 2\n", "10\n"},
		{"v2-unit-overflow", true, fmt.Sprintf("usage_usec %d\n", uint64(math.MaxUint64)/1000+1), "10\n"},
		{"v2-scan-error", true, "usage_usec 1\n" + strings.Repeat("x", 100000), "10\n"},
		{"v2-memory-missing", true, "usage_usec 1\n", ""},
		{"v2-memory-negative", true, "usage_usec 1\n", "-1\n"},
		{"v2-memory-empty", true, "usage_usec 1\n", "\n"},
		{"v2-memory-overflow", true, "usage_usec 1\n", "18446744073709551616\n"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			cpuPath, memoryPath := "cpu,cpuacct/cpuacct.usage", "memory/memory.usage_in_bytes"
			if test.v2 {
				cgroupFile(t, root, "cgroup.controllers", "cpu memory\n")
				cpuPath, memoryPath = "cpu.stat", "memory.current"
			}
			if test.cpu != "" {
				cgroupFile(t, root, cpuPath, test.cpu)
			}
			if test.memory != "" {
				cgroupFile(t, root, memoryPath, test.memory)
			}
			_, err := cgroupReader(root)()
			require.Error(t, err)
		})
	}
}

func TestCgroupZeroUsageIsValid(t *testing.T) {
	root := t.TempDir()
	cgroupFile(t, root, "cgroup.controllers", "cpu memory\n")
	cgroupFile(t, root, "cpu.stat", "usage_usec 0\n")
	cgroupFile(t, root, "memory.current", "0\n")
	sample, err := cgroupReader(root)()
	require.NoError(t, err)
	assert.Equal(t, resourceSample{}, sample)
}
