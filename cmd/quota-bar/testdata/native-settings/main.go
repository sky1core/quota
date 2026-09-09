package main

/*
#include <pthread.h>
#include <stdlib.h>
#include <stdint.h>
void native_check_start(void);
void native_check_schedule(void);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/getlantern/systray"
)

const example = `{"refreshActiveMinutes":3,"refreshIdleMinutes":30,"showResetTime":false,"startAtLogin":false,"selected":["claude_session","unavailable_key"],"availableItems":[{"key":"claude_session","label":"Claude · Session"},{"key":"codex_weekly","label":"Codex · 7d"}],"accounts":[{"provider":"claude","key":"claude","configDir":"","minLeftPct":null},{"provider":"codex","key":"codex","configDir":"","minLeftPct":10},{"provider":"claude","key":"claude-2","configDir":"~/example-account","minLeftPct":5}],"keepalive":{"enabled":false,"weekdays":["Mon","Tue","Wed","Thu","Fri"],"time":"12:30","idleMinutes":5,"activityMinutes":50,"message":"Reply with OK only."}}`

var calls atomic.Int32

func check(ok bool, reason string) {
	if !ok {
		fmt.Fprintln(os.Stderr, "FAIL:", reason)
		os.Exit(1)
	}
}

func apply(raw string) error {
	check(C.pthread_main_np() == 0, "callback must be off UI main thread")
	var value settingsWindowSnapshot
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		fmt.Fprintf(os.Stderr, "edited JSON: %v raw=%s\n", err, raw)
		check(false, "edited JSON")
	}
	check(value.RefreshActiveMinutes == 7, "edited active interval")
	check(len(value.Accounts) == 3, "account add/remove round trip")
	check(len(value.Selected) == 2 && value.Selected[0] == "unavailable_key" && value.Selected[1] == "codex_weekly", "hidden selection preservation and checkbox changes")
	check(value.Keepalive.Message == "Edited message.", "message editing")
	n := calls.Add(1)
	time.Sleep(350 * time.Millisecond)
	if n == 1 {
		return fmt.Errorf("Test apply error: retry without losing edits.")
	}
	return nil
}

//export nativeCheckReopen
func nativeCheckReopen() {
	check(openSettingsWindow(example, apply) == nil, "reopen")
}

//export nativeCheckFinish
func nativeCheckFinish() {
	check(calls.Load() == 2, "cancel must not invoke callback")
	settingsWindowCallbacks.Lock()
	count := len(settingsWindowCallbacks.byID)
	settingsWindowCallbacks.Unlock()
	check(count == 0, "all callback registrations released")
	fmt.Println("PASS: Go callback goroutine, edited JSON, failure/retry, cancellation, callback cleanup")
	go systray.Quit()
}

func main() {
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"accounts":[{"provider":"other"}]}`, `{"availableItems":[{"key":"","label":"x"}]}`} {
		check(openSettingsWindow(raw, apply) != nil, "reject malformed snapshot: "+raw)
	}
	check(openSettingsWindow(example, nil) != nil, "nil callback")
	systray.Run(func() {
		C.native_check_start()
		check(openSettingsWindow(example, apply) == nil, "open")
		check(openSettingsWindow(example, func(string) error { check(false, "duplicate open replaced callback"); return nil }) == nil, "duplicate open")
		C.native_check_schedule()
	}, func() { fmt.Println("PASS: original systray delegate and loop exited normally") })
}
