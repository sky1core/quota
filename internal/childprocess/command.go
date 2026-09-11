package childprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 100 * time.Millisecond
	return cmd
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func Run(cmd *exec.Cmd) error {
	defer killGroup(cmd)
	return cmd.Run()
}

func Output(cmd *exec.Cmd) ([]byte, error) {
	defer killGroup(cmd)
	return cmd.Output()
}

func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	defer killGroup(cmd)
	return cmd.CombinedOutput()
}
