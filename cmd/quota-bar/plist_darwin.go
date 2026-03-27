package main

// Embed Info.plist into the binary so macOS TCC can identify this app
// with a stable bundle ID (com.sky1core.quota-bar) instead of "a.out".

/*
#cgo LDFLAGS: -Wl,-sectcreate,__TEXT,__info_plist,${SRCDIR}/Info.plist
*/
import "C"
