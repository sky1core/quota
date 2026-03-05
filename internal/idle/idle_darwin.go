package idle

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>
*/
import "C"

// Seconds returns the number of seconds since the last user input event
// (keyboard, mouse, etc.) on macOS.
func Seconds() float64 {
	return float64(C.CGEventSourceSecondsSinceLastEventType(
		C.kCGEventSourceStateCombinedSessionState,
		C.kCGAnyInputEventType,
	))
}
