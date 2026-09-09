package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "settings_window_darwin.h"
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unsafe"
)

type settingsWindowSnapshot struct {
	RefreshActiveMinutes int                     `json:"refreshActiveMinutes"`
	RefreshIdleMinutes   int                     `json:"refreshIdleMinutes"`
	ShowResetTime        bool                    `json:"showResetTime"`
	StartAtLogin         bool                    `json:"startAtLogin"`
	Selected             []string                `json:"selected"`
	AvailableItems       []settingsWindowItem    `json:"availableItems"`
	Accounts             []settingsWindowAccount `json:"accounts"`
	Keepalive            settingsWindowKeepalive `json:"keepalive"`
}

type settingsWindowItem struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type settingsWindowAccount struct {
	Provider   string   `json:"provider"`
	Key        string   `json:"key"`
	ConfigDir  string   `json:"configDir"`
	MinLeftPct *float64 `json:"minLeftPct"`
}

type settingsWindowKeepalive struct {
	Enabled         bool     `json:"enabled"`
	Weekdays        []string `json:"weekdays"`
	Time            string   `json:"time"`
	IdleMinutes     int      `json:"idleMinutes"`
	ActivityMinutes int      `json:"activityMinutes"`
	Message         string   `json:"message"`
}

var settingsWindowCallbacks = struct {
	sync.Mutex
	next uint64
	byID map[uint64]func(string) error
}{byID: make(map[uint64]func(string) error)}

func openSettingsWindow(snapshotJSON string, callback func(string) error) error {
	if callback == nil {
		return fmt.Errorf("settings apply callback is required")
	}
	var snapshot settingsWindowSnapshot
	decoder := json.NewDecoder(strings.NewReader(snapshotJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return fmt.Errorf("settings snapshot: %w", err)
	}
	if !json.Valid([]byte(snapshotJSON)) || strings.TrimSpace(snapshotJSON) == "null" {
		return fmt.Errorf("settings snapshot must be a JSON object")
	}
	seen := make(map[string]bool)
	for _, item := range snapshot.AvailableItems {
		if item.Key == "" || item.Label == "" || seen[item.Key] {
			return fmt.Errorf("display items require unique keys and nonempty labels")
		}
		seen[item.Key] = true
	}
	for _, account := range snapshot.Accounts {
		if account.Provider != "claude" && account.Provider != "codex" {
			return fmt.Errorf("unsupported account provider %q", account.Provider)
		}
	}
	if snapshot.Selected == nil {
		snapshot.Selected = []string{}
	}
	if snapshot.AvailableItems == nil {
		snapshot.AvailableItems = []settingsWindowItem{}
	}
	if snapshot.Accounts == nil {
		snapshot.Accounts = []settingsWindowAccount{}
	}
	if snapshot.Keepalive.Weekdays == nil {
		snapshot.Keepalive.Weekdays = []string{}
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("settings snapshot: %w", err)
	}
	settingsWindowCallbacks.Lock()
	settingsWindowCallbacks.next++
	id := settingsWindowCallbacks.next
	settingsWindowCallbacks.byID[id] = callback
	settingsWindowCallbacks.Unlock()
	value := C.CString(string(data))
	defer C.free(unsafe.Pointer(value))
	C.quota_settings_open(C.uint64_t(id), value)
	return nil
}

//export quotaSettingsApply
func quotaSettingsApply(token C.uint64_t, value *C.char) {
	id, edited := uint64(token), C.GoString(value)
	settingsWindowCallbacks.Lock()
	callback := settingsWindowCallbacks.byID[id]
	settingsWindowCallbacks.Unlock()
	go func() {
		var applyErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				applyErr = fmt.Errorf("settings apply failed: %v", recovered)
			}
			message := ""
			if applyErr != nil {
				message = applyErr.Error()
				if message == "" {
					message = "Settings could not be applied."
				}
			}
			result := C.CString(strings.ToValidUTF8(strings.ReplaceAll(message, "\x00", "�"), "�"))
			defer C.free(unsafe.Pointer(result))
			C.quota_settings_complete(token, result)
		}()
		if callback == nil {
			applyErr = fmt.Errorf("settings window is no longer active")
			return
		}
		applyErr = callback(edited)
	}()
}

//export quotaSettingsClosed
func quotaSettingsClosed(token C.uint64_t) {
	settingsWindowCallbacks.Lock()
	delete(settingsWindowCallbacks.byID, uint64(token))
	settingsWindowCallbacks.Unlock()
}
