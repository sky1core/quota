package agentoverlay

import (
	"fmt"
	"os/exec"
)

type VerifyResult struct {
	Runtimes []RuntimeDoctor `json:"runtimes"`
	Failed   bool            `json:"failed"`
}

type LiveResult struct {
	Command  []string `json:"command"`
	ExitCode int      `json:"exitCode"`
	Output   string   `json:"output,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Verify runs the doctor entry checks for every runtime and, for each configured
// runtime whose entries are installed, runs that runtime's verify command. A
// runtime is enforced only when its verify command exits 0. A configured runtime
// with no verify command, a failed command, or missing entries stays degraded.
// It fails unless every configured runtime is enforced.
func Verify(spec *Spec) VerifyResult {
	var res VerifyResult
	for _, doc := range []RuntimeDoctor{DoctorClaude(spec), DoctorCodex(spec)} {
		switch doc.State {
		case StateUnconfigured:
			// Not configured for this runtime; nothing to enforce.
		case StateError, StateDegraded:
			res.Failed = true
		case StateInstalled:
			command := runtimeVerifyCommand(spec, doc.Runtime)
			if len(command) == 0 {
				doc.State = StateDegraded
				doc.Reason = "live verification not configured"
				res.Failed = true
				break
			}
			live := runLive(command)
			doc.Verify = &live
			if live.ExitCode == 0 {
				doc.State = StateEnforced
			} else {
				doc.State = StateDegraded
				doc.Reason = fmt.Sprintf("live verification failed (exit %d)", live.ExitCode)
				res.Failed = true
			}
		}
		res.Runtimes = append(res.Runtimes, doc)
	}
	return res
}

func runtimeVerifyCommand(spec *Spec, runtime string) []string {
	if spec.Verify == nil {
		return nil
	}
	switch runtime {
	case "claude":
		if spec.Verify.Claude != nil {
			return spec.Verify.Claude.Command
		}
	case "codex":
		if spec.Verify.Codex != nil {
			return spec.Verify.Codex.Command
		}
	}
	return nil
}

func runLive(command []string) LiveResult {
	live := LiveResult{Command: command}
	cmd := exec.Command(command[0], command[1:]...)
	out, err := cmd.CombinedOutput()
	live.Output = string(out)
	if err == nil {
		return live
	}
	if exit, ok := err.(*exec.ExitError); ok {
		live.ExitCode = exit.ExitCode()
		return live
	}
	live.ExitCode = -1
	live.Error = err.Error()
	return live
}
