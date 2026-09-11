#import "menu_order.source"

static NSMenu *testMenu;
static id testObserver;
static id failureObserver;
static NSMenuItem *extraItem;

void native_menu_fail_next_block(void) {
    quotaMenuOnMain(^{
        failureObserver = [NSNotificationCenter.defaultCenter
            addObserverForName:NSMenuDidAddItemNotification object:testMenu queue:nil
            usingBlock:^(NSNotification *notification) {
                if (extraItem != nil) return;
                extraItem = [[NSMenuItem alloc] initWithTitle:@"Injected extra entry"
                    action:@selector(menuHandler:) keyEquivalent:@""];
                extraItem.target = NSApp.delegate;
                extraItem.hidden = YES;
                [testMenu addItem:extraItem];
            }];
    });
}

void native_menu_clear_failure(void) {
    quotaMenuOnMain(^{
        [NSNotificationCenter.defaultCenter removeObserver:failureObserver];
        failureObserver = nil;
        [extraItem.menu removeItem:extraItem];
        extraItem = nil;
    });
}

void native_menu_watch(void) {
    quotaMenuOnMain(^{
        testObserver = [NSNotificationCenter.defaultCenter
            addObserverForName:NSMenuDidAddItemNotification object:nil queue:nil
            usingBlock:^(NSNotification *notification) {
                NSMenu *menu = notification.object;
                if (testMenu == nil && menu.supermenu == nil) testMenu = menu;
            }];
    });
}

char *native_menu_snapshot(void) {
    __block char *result;
    quotaMenuOnMain(^{
        NSMutableArray *rows = [NSMutableArray array];
        for (NSMenuItem *item in testMenu.itemArray) {
            if (item.hidden || item.separatorItem) continue;
            NSMutableArray *children = [NSMutableArray array];
            for (NSMenuItem *child in item.submenu.itemArray) {
                if (!child.hidden) [children addObject:child.title];
            }
            [rows addObject:@{@"title":item.title, @"tag":@(item.tag),
                @"checked":[NSNumber numberWithBool:item.state == NSControlStateValueOn],
                @"enabled":[NSNumber numberWithBool:item.enabled], @"submenu":[NSNumber numberWithBool:item.hasSubmenu],
                @"children":children}];
        }
        NSData *data = [NSJSONSerialization dataWithJSONObject:
            @{@"rows":rows, @"count":@(testMenu.numberOfItems)} options:0 error:nil];
        result = strdup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding].UTF8String);
    });
    return result;
}

void native_menu_click(long tag) {
    quotaMenuOnMain(^{
        NSMenuItem *item = [testMenu itemWithTag:tag];
        [NSApp sendAction:item.action to:item.target from:item];
    });
}

bool native_menu_present(void) {
    __block bool tracked = false;
    quotaMenuOnMain(^{
        id observer = [NSNotificationCenter.defaultCenter
            addObserverForName:NSMenuDidBeginTrackingNotification object:testMenu queue:nil
            usingBlock:^(NSNotification *notification) { tracked = true; }];
        NSTimer *timer = [NSTimer timerWithTimeInterval:0.3 repeats:NO block:^(NSTimer *timer) {
            [testMenu cancelTrackingWithoutAnimation];
        }];
        [NSRunLoop.mainRunLoop addTimer:timer forMode:NSRunLoopCommonModes];
        [testMenu popUpMenuPositioningItem:nil atLocation:NSMakePoint(80, 500) inView:nil];
        [timer invalidate];
        [NSNotificationCenter.defaultCenter removeObserver:observer];
    });
    return tracked;
}
