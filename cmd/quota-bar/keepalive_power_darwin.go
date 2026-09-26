package main

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/pwr_mgt/IOPMLib.h>
#include <IOKit/ps/IOPowerSources.h>

static int quotaExternalPower(void) {
    CFTypeRef info = IOPSCopyPowerSourcesInfo();
    if (info == NULL) return -1;
    CFStringRef source = IOPSGetProvidingPowerSourceType(info);
    int result = -1;
    if (source != NULL) result = CFEqual(source, CFSTR(kIOPMACPowerKey)) ? 1 : 0;
    CFRelease(info);
    return result;
}

static IOReturn quotaPreventSystemSleep(IOPMAssertionID *id) {
    return IOPMAssertionCreateWithName(kIOPMAssertionTypePreventSystemSleep,
        kIOPMAssertionLevelOn, CFSTR("quota-bar keepalive"), id);
}

static IOReturn quotaAllowSystemSleep(IOPMAssertionID id) {
    return IOPMAssertionRelease(id);
}
*/
import "C"

import "errors"

type systemSleepGuard struct {
	id     C.IOPMAssertionID
	active bool
}

func externalPower() int {
	return int(C.quotaExternalPower())
}

func (g *systemSleepGuard) sync(want bool) (string, error) {
	if !want {
		if err := g.close(); err != nil {
			return "unavailable", err
		}
		return "off", nil
	}
	switch externalPower() {
	case 0:
		if err := g.close(); err != nil {
			return "unavailable", err
		}
		return "battery", nil
	case 1:
		if g.active {
			return "active", nil
		}
		if C.quotaPreventSystemSleep(&g.id) != C.kIOReturnSuccess {
			return "unavailable", errors.New("system sleep prevention failed")
		}
		g.active = true
		return "active", nil
	default:
		return "unknown", errors.Join(errors.New("power source unavailable"), g.close())
	}
}

func (g *systemSleepGuard) close() error {
	if !g.active {
		return nil
	}
	if C.quotaAllowSystemSleep(g.id) != C.kIOReturnSuccess {
		return errors.New("system sleep prevention release failed")
	}
	g.active = false
	return nil
}
