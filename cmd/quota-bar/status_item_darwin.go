package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include "status_item_darwin.h"
*/
import "C"

import "errors"

// pinStatusItem gives the systray status item a fixed autosave name and
// forces it visible, so a menu bar hide that macOS persisted under the
// app's defaults does not keep the item hidden across restarts.
func pinStatusItem() error {
	if !bool(C.quota_status_item_pin()) {
		return errors.New("could not pin the menu bar status item")
	}
	return nil
}
