#import <Cocoa/Cocoa.h>
#include "status_item_darwin.h"

static void quotaStatusItemOnMain(void (^work)(void)) {
    if (NSThread.isMainThread) work();
    else dispatch_sync(dispatch_get_main_queue(), work);
}

bool quota_status_item_pin(void) {
    __block bool pinned = false;
    quotaStatusItemOnMain(^{
        id item = nil;
        @try {
            item = [(NSObject *)NSApp.delegate valueForKey:@"statusItem"];
        } @catch (NSException *exception) {
            return;
        }
        if (![item isKindOfClass:[NSStatusItem class]]) return;
        NSStatusItem *statusItem = item;
        statusItem.autosaveName = @"quota-bar";
        statusItem.visible = YES;
        pinned = statusItem.visible;
    });
    return pinned;
}
