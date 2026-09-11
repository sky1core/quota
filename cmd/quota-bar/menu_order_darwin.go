package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include "menu_order_darwin.h"
*/
import "C"

import "fmt"

func beginMenuBlock() error {
	if !bool(C.quota_menu_begin_block()) {
		return fmt.Errorf("another account menu block is being created")
	}
	return nil
}

func endMenuBlockAt(index, count int) error {
	if !bool(C.quota_menu_end_block(C.size_t(index), C.size_t(count))) {
		return fmt.Errorf("could not place account menu entries at index %d", index)
	}
	return nil
}
