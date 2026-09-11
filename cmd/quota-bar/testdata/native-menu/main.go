package main

/*
#include <stdlib.h>
#include <stdbool.h>
void native_menu_watch(void);
char *native_menu_snapshot(void);
void native_menu_click(long tag);
bool native_menu_present(void);
void native_menu_fail_next_block(void);
void native_menu_clear_failure(void);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"
	"unsafe"

	"github.com/getlantern/systray"
)

type nativeRow struct {
	Title                     string
	Tag                       int
	Checked, Enabled, Submenu bool
	Children                  []string
}

type nativeMenu struct {
	Rows  []nativeRow
	Count int
}

func require(ok bool, message string) {
	if !ok {
		fmt.Fprintln(os.Stderr, "FAIL:", message)
		os.Exit(1)
	}
}

func snapshot() nativeMenu {
	data := C.native_menu_snapshot()
	defer C.free(unsafe.Pointer(data))
	var result nativeMenu
	if err := json.Unmarshal([]byte(C.GoString(data)), &result); err != nil {
		require(false, "native menu JSON: "+err.Error())
	}
	return result
}

func verify(m *accountMenus, entries []accountMenuSpec, selected string) nativeMenu {
	require(m.reconcile(entries, func(key string) bool { return key == selected }) == nil, "reconcile")
	want := []string{}
	for _, a := range entries {
		want = append(want, "── "+a.label+" ──")
		for _, w := range a.windows {
			want = append(want, itemKey(a.key, w))
		}
	}
	for _, item := range m.items {
		item.item.SetTitle(item.key)
		item.item.Show()
	}
	want = append(want, "Refresh", "Settings…", "Quit")
	native := snapshot()
	got := []string{}
	for _, row := range native.Rows {
		got = append(got, row.Title)
		require(!row.Submenu, "quota rows must be on the root menu: "+row.Title)
		if strings.Contains(row.Title, "_") {
			require(row.Checked == (row.Title == selected), "checkbox state: "+row.Title)
			require(row.Enabled, "quota row must be clickable")
		}
	}
	require(reflect.DeepEqual(got, want), fmt.Sprintf("root order: got %v, want %v", got, want))
	require(reflect.DeepEqual(m.keys, func() []string {
		var keys []string
		for _, a := range entries {
			for _, w := range a.windows {
				keys = append(keys, itemKey(a.key, w))
			}
		}
		return keys
	}()), "selection order follows visible accounts")
	return native
}

func main() {
	systray.Run(func() {
		C.native_menu_watch()
		m := newAccountMenus()
		claude := accountMenuSpec{"claude", "claude", "Claude", []string{"session", "weekly_all"}}
		codex := accountMenuSpec{"codex", "codex", "Codex", []string{"5h", "weekly"}}
		claude2 := accountMenuSpec{"claude", "claude-2", "Claude 2", claude.windows}
		claude3 := accountMenuSpec{"claude", "claude-3", "Claude 3", claude.windows}
		codex2 := accountMenuSpec{"codex", "codex-2", "Codex 2", codex.windows}
		initial := []accountMenuSpec{claude, codex}
		require(m.reconcile(initial, func(string) bool { return false }) == nil, "initial creation")
		systray.AddSeparator()
		for _, title := range []string{"Refresh", "Settings…", "Quit"} {
			systray.AddMenuItem(title, "")
		}
		verify(m, initial, "codex_5h")
		fmt.Println("PASS: all account quota rows are visible at root, checkbox selection preserved")
		many := []accountMenuSpec{claude, claude2, claude3, codex, codex2}
		beforeFailure := snapshot()
		beforeKeys := append([]string(nil), m.keys...)
		beforeCodex := m.resets["codex"]
		C.native_menu_fail_next_block()
		require(m.reconcile(many, func(string) bool { return false }) != nil, "injected native placement failure")
		failedCount := snapshot().Count
		for range 5 {
			require(m.reconcile(many, func(string) bool { return false }) != nil, "placement failure remains explicit")
			require(snapshot().Count == failedCount, "repeated placement failures must reuse the pending block")
		}
		C.native_menu_clear_failure()
		require(reflect.DeepEqual(snapshot().Rows, beforeFailure.Rows), "native placement failure preserves visible menu")
		require(reflect.DeepEqual(m.keys, beforeKeys) && m.resets["codex"] == beforeCodex, "native placement failure preserves active mappings")
		fmt.Println("PASS: repeated native placement failures preserve the menu and reuse pending rows")
		expanded := verify(m, many, "claude-2_session")
		require(expanded.Count == beforeFailure.Count+13, "recovered placement must not leave orphan menu rows")
		verify(m, []accountMenuSpec{claude, codex2}, "codex-2_weekly")
		verify(m, []accountMenuSpec{codex}, "codex_5h")
		for range 5 {
			verify(m, initial, "codex_5h")
			verify(m, many, "claude-2_session")
		}
		require(snapshot().Count == expanded.Count, "repeated account edits must reuse menu rows")
		fmt.Println("PASS: account add/remove/re-add stays above common commands, no duplicate rows")
		m.errors["claude-2"].SetTitle("Example quota error")
		m.errors["claude-2"].Show()
		m.resets["codex"].SetTitle("Reset credits: 1")
		m.resets["codex"].Show()
		m.resetChildren["codex"][0].SetTitle("1d 0h")
		m.resetChildren["codex"][0].Show()
		errorFound, resetFound := false, false
		for _, row := range snapshot().Rows {
			if row.Title == "Example quota error" {
				errorFound = true
				require(!row.Submenu, "error visible on root")
			}
			if row.Title == "Reset credits: 1" {
				resetFound = true
				require(row.Submenu && reflect.DeepEqual(row.Children, []string{"1d 0h"}), "reset credit details remain nested")
			}
		}
		require(errorFound && resetFound, "errors and reset credits remain visible")
		verify(m, many, "claude-2_session")
		var click menuItem
		for _, item := range m.items {
			if item.key == "claude-2_session" {
				click = item
			}
		}
		var tag int
		for _, row := range snapshot().Rows {
			if row.Title == click.key {
				tag = row.Tag
			}
		}
		clicked := make(chan struct{})
		go func() { <-click.item.ClickedCh; close(clicked) }()
		deadline := time.After(time.Second)
		for {
			C.native_menu_click(C.long(tag))
			select {
			case <-clicked:
				fmt.Println("PASS: native click reaches original Go channel after menu moves; errors and reset details preserved")
				require(bool(C.native_menu_present()), "native menu must open for tracking")
				fmt.Println("PASS: native menu opened and closed")
				systray.Quit()
				return
			case <-deadline:
				require(false, "native action did not reach checkbox channel")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}, func() { fmt.Println("PASS: native menu loop closed") })
}
