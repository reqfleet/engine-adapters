//go:build integration && linux

package conformance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	supervisorArgument = "--conformance-process-supervisor"
	processCleanupWait = 5 * time.Second
)

// A separate subreaper owns each scenario. Engine process groups (and orphaned
// grandchildren) cannot escape it, and the test process never reaps other tests'
// children. This needs neither writable cgroups nor privileged PID namespaces.
func init() {
	if len(os.Args) != 3 || os.Args[1] != supervisorArgument {
		return
	}
	if err := superviseRuntime(os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type runtimeProcess struct {
	command *exec.Cmd
	stop    *os.File
	done    chan error
	exited  chan struct{}
	pid     int
}

func launchRuntimeProcess(executable string, environment []string, logs io.Writer) (*runtimeProcess, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer controlRead.Close()
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		controlWrite.Close()
		return nil, err
	}
	defer statusWrite.Close()
	command := exec.Command(self, supervisorArgument, executable)
	command.Env = environment
	command.Stdin = controlRead
	command.Stdout, command.Stderr = logs, logs
	command.ExtraFiles = []*os.File{statusWrite}
	command.WaitDelay = time.Second
	process := &runtimeProcess{command: command, stop: controlWrite, done: make(chan error, 1), exited: make(chan struct{})}
	if err := command.Start(); err != nil {
		controlWrite.Close()
		statusRead.Close()
		return nil, err
	}
	// The child has inherited its copies; retaining ours would mask EOF when
	// the supervisor fails before publishing the adapter PID.
	_ = controlRead.Close()
	_ = statusWrite.Close()
	go func() { process.done <- command.Wait() }()
	started := make(chan error, 1)
	go func() {
		defer statusRead.Close()
		defer close(process.exited)
		decoder := json.NewDecoder(statusRead)
		err := decoder.Decode(&process.pid)
		started <- err
		if err == nil {
			var exited bool
			_ = decoder.Decode(&exited)
		}
	}()
	select {
	case err = <-started:
	case <-time.After(processCleanupWait):
		err = fmt.Errorf("adapter supervisor did not start within %s", processCleanupWait)
	}
	if err != nil {
		if cleanupErr := process.close(); cleanupErr != nil {
			abortRuntimeCleanup(cleanupErr)
		}
		return nil, err
	}
	return process, nil
}

func (p *runtimeProcess) close() error {
	_ = p.stop.Close()
	select {
	case err := <-p.done:
		return err
	case <-time.After(processCleanupWait + 2*time.Second):
		return fmt.Errorf("adapter supervisor %d did not finish cleanup", p.command.Process.Pid)
	}
}

func abortRuntimeCleanup(err error) {
	// Continuing after unconfirmed cleanup would let the next scenario inherit
	// workload or listeners. The disposable container must stop instead.
	fmt.Fprintf(os.Stderr, "unsafe to continue conformance after process cleanup: %v\n", err)
	os.Exit(1)
}

func superviseRuntime(executable string) error {
	if os.Geteuid() != 0 {
		return errors.New("adapter supervisor requires the root container harness")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return fmt.Errorf("adapter supervisor requires a disposable Docker container: %w", err)
	}
	const prSetChildSubreaper = 36
	if _, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("enable scenario subreaper: %w", errno)
	}
	status := os.NewFile(3, "adapter-status")
	syscall.CloseOnExec(3)
	defer status.Close()
	encoder := json.NewEncoder(status)
	child := exec.Command(executable)
	child.Env = os.Environ()
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65532, Gid: 65532}}
	if err := child.Start(); err != nil {
		return err
	}
	// Only this supervisor waits; exec.Cmd.Wait is deliberately not used here.
	defer child.Process.Release()
	if err := encoder.Encode(child.Process.Pid); err != nil {
		return errors.Join(err, killRuntimeChildren())
	}
	stop := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(stop)
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return killRuntimeChildren()
		case <-ticker.C:
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(child.Process.Pid, &status, syscall.WNOHANG, nil)
			if err != nil && !errors.Is(err, syscall.EINTR) {
				return errors.Join(err, killRuntimeChildren())
			}
			if pid == child.Process.Pid {
				_ = encoder.Encode(true)
				return killRuntimeChildren()
			}
		}
	}
}

func killRuntimeChildren() error {
	deadline := time.Now().Add(processCleanupWait)
	for time.Now().Before(deadline) {
		// Children belong to the thread that forked them, not necessarily the
		// thread-group leader. No other goroutine in this process reaps them,
		// so their PIDs cannot be recycled between this read and SIGKILL.
		tasks, err := os.ReadDir("/proc/self/task")
		if err != nil {
			return err
		}
		for _, task := range tasks {
			data, err := os.ReadFile(filepath.Join("/proc/self/task", task.Name(), "children"))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			for _, field := range strings.Fields(string(data)) {
				pid, err := strconv.Atoi(field)
				if err != nil {
					return err
				}
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					return err
				}
			}
		}
		for time.Now().Before(deadline) {
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if errors.Is(err, syscall.ECHILD) {
				return nil // Includes all descendants adopted from killed parents.
			}
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil {
				return err
			}
			if pid == 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("scenario descendants remained after %s", processCleanupWait)
}
