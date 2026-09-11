#import <Cocoa/Cocoa.h>
#include "menu_order_darwin.h"

static id quotaMenuObserver;
static NSMutableArray<NSMenuItem *> *quotaMenuAddedItems;

static void quotaMenuOnMain(void (^work)(void)) {
    if (NSThread.isMainThread) work();
    else dispatch_sync(dispatch_get_main_queue(), work);
}

bool quota_menu_begin_block(void) {
    __block bool started = false;
    quotaMenuOnMain(^{
        if (quotaMenuObserver != nil || quotaMenuAddedItems != nil) return;
        quotaMenuAddedItems = [NSMutableArray array];
        quotaMenuObserver = [NSNotificationCenter.defaultCenter
            addObserverForName:NSMenuDidAddItemNotification object:nil queue:nil
            usingBlock:^(NSNotification *notification) {
                NSMenu *menu = notification.object;
                if (menu.supermenu != nil) return;
                NSInteger index = [notification.userInfo[@"NSMenuItemIndex"] integerValue];
                [quotaMenuAddedItems addObject:[menu itemAtIndex:index]];
            }];
        started = true;
    });
    return started;
}

bool quota_menu_end_block(size_t index, size_t count) {
    __block bool placed = false;
    quotaMenuOnMain(^{
        if (quotaMenuObserver != nil) {
            [NSNotificationCenter.defaultCenter removeObserver:quotaMenuObserver];
            quotaMenuObserver = nil;
        }
        NSMutableArray<NSMenuItem *> *items = [NSMutableArray array];
        for (NSMenuItem *item in quotaMenuAddedItems) {
            if (item.target == NSApp.delegate && item.action != NULL && item.menu != nil && item.menu.supermenu == nil)
                [items addObject:item];
        }
        if (items.count != count || count == 0) return;
        NSMenu *menu = items.firstObject.menu;
        if (menu == nil || index > menu.numberOfItems - count) return;
        for (NSMenuItem *item in items) if (item.menu != menu) return;
        for (NSMenuItem *item in items) [menu removeItem:item];
        NSInteger position = (NSInteger)index;
        for (NSMenuItem *item in items) [menu insertItem:item atIndex:position++];
        quotaMenuAddedItems = nil;
        placed = true;
    });
    return placed;
}
