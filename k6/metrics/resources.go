// Adapted from github.com/reqfleet/engine-sdk v0.3.0,
// internal/containerstats/cgroup.go. Copyright reqfleet contributors.
// Licensed under the Apache License, Version 2.0; see LICENSE and NOTICE.
// Modifications: explicit errors, injectable root, strict parsing, alternate v1
// mounts, and conversion of v2 microseconds to v1-compatible nanoseconds.
package metrics

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type resourceSample struct {
	cpuNanos    uint64
	memoryBytes uint64
}

func cgroupReader(root string) func() (resourceSample, error) {
	return func() (resourceSample, error) {
		var sample resourceSample
		_, err := os.Stat(filepath.Join(root, "cgroup.controllers"))
		switch {
		case err == nil:
			micros, err := cpuMicroseconds(filepath.Join(root, "cpu.stat"))
			if err != nil {
				return sample, err
			}
			if micros > math.MaxUint64/1000 {
				return sample, errors.New("cgroup CPU microseconds overflow nanoseconds")
			}
			sample.cpuNanos = micros * 1000
			sample.memoryBytes, err = unsignedFile(filepath.Join(root, "memory.current"))
			return sample, err
		case errors.Is(err, os.ErrNotExist):
			cpuPath := filepath.Join(root, "cpu,cpuacct", "cpuacct.usage")
			sample.cpuNanos, err = unsignedFile(cpuPath)
			if errors.Is(err, os.ErrNotExist) {
				sample.cpuNanos, err = unsignedFile(filepath.Join(root, "cpuacct", "cpuacct.usage"))
			}
			if err != nil {
				return sample, err
			}
			sample.memoryBytes, err = unsignedFile(filepath.Join(root, "memory", "memory.usage_in_bytes"))
			return sample, err
		default:
			return sample, fmt.Errorf("detect cgroup version: %w", err)
		}
	}
}

func unsignedValue(value string) (uint64, error) {
	if value == "" {
		return 0, errors.New("empty cgroup value")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("invalid cgroup unsigned integer %q", value)
		}
	}
	return strconv.ParseUint(value, 10, 64)
}

func unsignedFile(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := unsignedValue(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return value, nil
}

func cpuMicroseconds(path string) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var value uint64
	found := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "usage_usec" {
			continue
		}
		if len(fields) != 2 || found {
			return 0, fmt.Errorf("invalid usage_usec in %s", path)
		}
		value, err = unsignedValue(fields[1])
		if err != nil {
			return 0, fmt.Errorf("%s: %w", path, err)
		}
		found = true
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	if !found {
		return 0, fmt.Errorf("usage_usec missing in %s", path)
	}
	return value, nil
}
