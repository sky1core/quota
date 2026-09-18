#import <Cocoa/Cocoa.h>
#include <math.h>
#include "settings_window_darwin.h"

@interface QuotaSettingsView : NSView
@end

@implementation QuotaSettingsView
- (BOOL)isFlipped { return YES; }
@end

@interface QuotaSettingsWindow : NSWindow
@end

@implementation QuotaSettingsWindow
- (BOOL)canBecomeKeyWindow { return YES; }
- (BOOL)canBecomeMainWindow { return YES; }
- (NSText *)fieldEditor:(BOOL)create forObject:(id)object {
    NSText *editor = [super fieldEditor:create forObject:object];
    if ([editor isKindOfClass:[NSTextView class]]) [(NSTextView *)editor setAllowsUndo:YES];
    return editor;
}
- (void)cancelOperation:(id)sender { [self performClose:sender]; }
- (BOOL)performKeyEquivalent:(NSEvent *)event {
    NSEventModifierFlags flags = event.modifierFlags & NSEventModifierFlagDeviceIndependentFlagsMask;
    if ((flags & ~NSEventModifierFlagShift) == NSEventModifierFlagCommand) {
        NSString *key = event.charactersIgnoringModifiers.lowercaseString;
        if ([key isEqualToString:@"w"]) {
            [self performClose:self];
            return YES;
        }
        if ([self.firstResponder isKindOfClass:[NSTextView class]]) {
            NSTextView *text = (NSTextView *)self.firstResponder;
            if ([key isEqualToString:@"z"]) {
                NSUndoManager *undo = text.undoManager;
                if (flags & NSEventModifierFlagShift) {
                    if (undo.canRedo) [undo redo];
                } else if (undo.canUndo) {
                    [undo undo];
                }
                return YES;
            }
            NSDictionary *actions = @{@"c": @"copy:", @"v": @"paste:", @"a": @"selectAll:", @"x": @"cut:"};
            NSString *action = actions[key];
            if (action && !(flags & NSEventModifierFlagShift)) {
                return [NSApp sendAction:NSSelectorFromString(action) to:text from:self];
            }
        }
    }
    return [super performKeyEquivalent:event];
}
@end

static NSTextField *QuotaLabel(NSView *view, NSString *title, NSRect frame) {
    NSTextField *label = [NSTextField labelWithString:title];
    label.frame = frame;
    [view addSubview:label];
    return label;
}

static NSTextField *QuotaField(NSView *view, NSString *value, NSString *name, NSRect frame) {
    NSTextField *field = [[NSTextField alloc] initWithFrame:frame];
    field.stringValue = value;
    field.accessibilityLabel = name;
    field.usesSingleLineMode = YES;
    field.cell.scrollable = YES;
    [view addSubview:field];
    return field;
}

static NSButton *QuotaButton(NSView *view, NSString *title, id target, SEL action, NSRect frame) {
    NSButton *button = [NSButton buttonWithTitle:title target:target action:action];
    button.frame = frame;
    [view addSubview:button];
    return button;
}

static NSButton *QuotaCheck(NSView *view, NSString *title, BOOL checked, NSRect frame) {
    NSButton *button = [NSButton checkboxWithTitle:title target:nil action:nil];
    button.frame = frame;
    button.state = checked ? NSControlStateValueOn : NSControlStateValueOff;
    [view addSubview:button];
    return button;
}

static NSScrollView *QuotaScroll(NSView *parent, NSView *document, NSRect frame) {
    NSScrollView *scroll = [[NSScrollView alloc] initWithFrame:frame];
    scroll.borderType = NSBezelBorder;
    scroll.hasVerticalScroller = YES;
    scroll.autohidesScrollers = YES;
    scroll.documentView = document;
    [parent addSubview:scroll];
    return scroll;
}

static BOOL QuotaInteger(NSTextField *field, NSInteger minimum, NSInteger maximum, NSInteger *value) {
    NSString *text = [field.stringValue stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceAndNewlineCharacterSet];
    if (text.length == 0 || [text rangeOfCharacterFromSet:[NSCharacterSet characterSetWithCharactersInString:@"0123456789"].invertedSet].location != NSNotFound) return NO;
    NSScanner *scanner = [NSScanner scannerWithString:text];
    long long parsed;
    if (![scanner scanLongLong:&parsed] || !scanner.isAtEnd || parsed < minimum || parsed > maximum) return NO;
    *value = (NSInteger)parsed;
    return YES;
}

static void QuotaShowSettingsWindowFront(NSWindow *window) {
    // Do not replace this with -activate: it is cooperative and can leave the window behind another app.
    [NSApp activateIgnoringOtherApps:YES];
    [window makeKeyAndOrderFront:nil];
}

@interface QuotaSettingsAccountRow : NSObject
@property NSView *view;
@property NSPopUpButton *provider;
@property NSTextField *key;
@property NSTextField *directory;
@property NSTextField *minimum;
@property NSButton *choose;
@property NSButton *remove;
@property BOOL reserved;
@end

@implementation QuotaSettingsAccountRow
@end

@interface QuotaSettingsController : NSWindowController <NSWindowDelegate, NSTabViewDelegate>
@property uint64_t token;
@property (nonatomic) BOOL applying;
@property NSDictionary *snapshot;
@property NSTabView *tabs;
@property NSTextField *activeMinutes;
@property NSTextField *idleMinutes;
@property NSButton *showReset;
@property NSButton *startAtLogin;
@property NSMutableArray<NSButton *> *displayChecks;
@property NSMutableArray<QuotaSettingsAccountRow *> *accounts;
@property QuotaSettingsView *accountsDocument;
@property NSScrollView *accountsScroll;
@property NSButton *addAccount;
@property NSButton *keepaliveEnabled;
@property NSMutableArray<NSButton *> *weekdays;
@property NSTextField *scheduledTime;
@property NSTextField *keepaliveIdle;
@property NSTextField *activityMinutes;
@property NSTextView *message;
@property NSPopUpButton *updateMode;
@property NSTextField *updateRef;
@property NSTextView *errorText;
@property NSScrollView *errorScroll;
@property NSButton *saveButton;
@property NSButton *cancelButton;
- (void)openSnapshot:(NSDictionary *)snapshot token:(uint64_t)token;
- (void)complete:(NSString *)error token:(uint64_t)token;
@end

@implementation QuotaSettingsController

- (instancetype)init {
    QuotaSettingsWindow *window = [[QuotaSettingsWindow alloc]
        initWithContentRect:NSMakeRect(0, 0, 800, 620)
        styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable
        backing:NSBackingStoreBuffered defer:NO];
    self = [super initWithWindow:window];
    if (self) {
        window.title = @"Quota Settings";
        window.releasedWhenClosed = NO;
        window.delegate = self;
        window.tabbingMode = NSWindowTabbingModeDisallowed;
        [window center];
    }
    return self;
}

- (QuotaSettingsView *)addTab:(NSString *)title {
    NSTabViewItem *item = [[NSTabViewItem alloc] initWithIdentifier:title];
    item.label = title;
    QuotaSettingsView *view = [[QuotaSettingsView alloc] initWithFrame:NSMakeRect(0, 0, 736, 428)];
    item.view = view;
    [self.tabs addTabViewItem:item];
    return view;
}

- (void)buildGeneral {
    NSView *view = [self addTab:@"General"];
    QuotaLabel(view, @"Refresh when active (minutes)", NSMakeRect(20, 22, 250, 22));
    self.activeMinutes = QuotaField(view, [self.snapshot[@"refreshActiveMinutes"] stringValue], @"Active refresh minutes", NSMakeRect(290, 18, 100, 26));
    QuotaLabel(view, @"Refresh when idle (minutes)", NSMakeRect(20, 64, 250, 22));
    self.idleMinutes = QuotaField(view, [self.snapshot[@"refreshIdleMinutes"] stringValue], @"Idle refresh minutes", NSMakeRect(290, 60, 100, 26));
    self.showReset = QuotaCheck(view, @"Show reset as clock time", [self.snapshot[@"showResetTime"] boolValue], NSMakeRect(20, 106, 340, 24));
    self.startAtLogin = QuotaCheck(view, @"Start at next login", [self.snapshot[@"startAtLogin"] boolValue], NSMakeRect(20, 142, 340, 24));
    QuotaLabel(view, @"Display in menu bar", NSMakeRect(20, 186, 500, 22));
    NSArray *items = self.snapshot[@"availableItems"];
    NSArray *selected = self.snapshot[@"selected"];
    QuotaSettingsView *document = [[QuotaSettingsView alloc] initWithFrame:NSMakeRect(0, 0, 680, MAX(170, items.count * 30 + 16))];
    self.displayChecks = [NSMutableArray array];
    for (NSDictionary *item in items) {
        NSButton *check = QuotaCheck(document, item[@"label"], [selected containsObject:item[@"key"]], NSMakeRect(10, 8 + self.displayChecks.count * 30, 650, 24));
        check.toolTip = item[@"label"];
        [self.displayChecks addObject:check];
    }
    if (items.count == 0) {
        QuotaLabel(document, @"No display items available yet.", NSMakeRect(12, 12, 620, 22));
    }
    QuotaScroll(view, document, NSMakeRect(20, 214, 694, 174));
}

- (void)layoutAccounts {
    CGFloat height = MAX(self.accountsScroll.contentSize.height, self.accounts.count * 38 + 12);
    self.accountsDocument.frame = NSMakeRect(0, 0, 698, height);
    [self.accounts enumerateObjectsUsingBlock:^(QuotaSettingsAccountRow *row, NSUInteger index, BOOL * __unused stop) {
        row.view.frame = NSMakeRect(4, 6 + index * 38, 690, 34);
    }];
    [self.window recalculateKeyViewLoop];
}

- (QuotaSettingsAccountRow *)appendAccount:(NSDictionary *)account {
    QuotaSettingsAccountRow *row = [[QuotaSettingsAccountRow alloc] init];
    row.view = [[QuotaSettingsView alloc] initWithFrame:NSMakeRect(0, 0, 690, 34)];
    row.provider = [[NSPopUpButton alloc] initWithFrame:NSMakeRect(0, 3, 94, 28) pullsDown:NO];
    [row.provider addItemsWithTitles:@[@"claude", @"codex"]];
    [row.provider selectItemWithTitle:account[@"provider"]];
    row.provider.accessibilityLabel = @"Account provider";
    [row.view addSubview:row.provider];
    row.key = QuotaField(row.view, account[@"key"], @"Account key", NSMakeRect(98, 5, 116, 24));
    row.directory = QuotaField(row.view, account[@"configDir"], @"Config directory", NSMakeRect(220, 5, 240, 24));
    row.choose = QuotaButton(row.view, @"…", self, @selector(chooseDirectory:), NSMakeRect(464, 2, 32, 30));
    row.choose.accessibilityLabel = @"Choose config directory";
    id minimum = account[@"minLeftPct"];
    row.minimum = QuotaField(row.view, minimum == NSNull.null ? @"" : [minimum stringValue], @"Minimum remaining percent", NSMakeRect(502, 5, 72, 24));
    row.minimum.placeholderString = @"5% default";
    row.minimum.toolTip = @"0–100; leave blank for the default (5%).";
    row.remove = QuotaButton(row.view, @"Remove", self, @selector(removeAccount:), NSMakeRect(580, 2, 106, 30));
    row.reserved = [row.key.stringValue isEqualToString:account[@"provider"]];
    if (row.reserved) {
        row.provider.enabled = NO;
        row.key.enabled = NO;
        row.directory.enabled = NO;
        row.directory.placeholderString = @"Default account directory";
        row.choose.enabled = NO;
        row.remove.enabled = NO;
    }
    [self.accounts addObject:row];
    [self.accountsDocument addSubview:row.view];
    return row;
}

- (void)buildAccounts {
    NSView *view = [self addTab:@"Accounts"];
    QuotaLabel(view, @"Register existing account directories. Sign in with the provider’s CLI.", NSMakeRect(16, 18, 710, 22));
    QuotaLabel(view, @"Provider", NSMakeRect(20, 56, 94, 22));
    QuotaLabel(view, @"Key", NSMakeRect(118, 56, 116, 22));
    QuotaLabel(view, @"Config directory", NSMakeRect(240, 56, 270, 22));
    QuotaLabel(view, @"Min left %", NSMakeRect(522, 56, 80, 22));
    self.accounts = [NSMutableArray array];
    self.accountsDocument = [[QuotaSettingsView alloc] initWithFrame:NSMakeRect(0, 0, 698, 248)];
    self.accountsScroll = QuotaScroll(view, self.accountsDocument, NSMakeRect(16, 82, 712, 248));
    for (NSDictionary *account in self.snapshot[@"accounts"]) [self appendAccount:account];
    [self layoutAccounts];
    self.addAccount = QuotaButton(view, @"Add Account", self, @selector(addAccount:), NSMakeRect(14, 344, 130, 32));
    NSTextField *hint = QuotaLabel(view, @"Default accounts stay registered. Blank minimum uses 5%.", NSMakeRect(156, 350, 568, 22));
    hint.textColor = NSColor.secondaryLabelColor;
}

- (void)buildKeepalive {
    NSView *view = [self addTab:@"Keepalive"];
    NSDictionary *config = self.snapshot[@"keepalive"];
    self.keepaliveEnabled = QuotaCheck(view, @"Enable keepalive", [config[@"enabled"] boolValue], NSMakeRect(20, 18, 350, 24));
    QuotaLabel(view, @"Days", NSMakeRect(20, 64, 130, 22));
    self.weekdays = [NSMutableArray array];
    for (NSString *day in @[@"Mon", @"Tue", @"Wed", @"Thu", @"Fri", @"Sat", @"Sun"]) {
        NSButton *button = QuotaCheck(view, day, [config[@"weekdays"] containsObject:day], NSMakeRect(162 + self.weekdays.count * 76, 62, 74, 24));
        [self.weekdays addObject:button];
    }
    QuotaLabel(view, @"Local time (HH:MM)", NSMakeRect(20, 108, 260, 22));
    self.scheduledTime = QuotaField(view, config[@"time"], @"Keepalive local time", NSMakeRect(300, 104, 100, 26));
    self.scheduledTime.placeholderString = @"HH:MM";
    QuotaLabel(view, @"PC idle for (minutes)", NSMakeRect(20, 152, 260, 22));
    self.keepaliveIdle = QuotaField(view, [config[@"idleMinutes"] stringValue], @"Keepalive idle minutes", NSMakeRect(300, 148, 100, 26));
    QuotaLabel(view, @"Work completed within (minutes)", NSMakeRect(20, 196, 260, 22));
    self.activityMinutes = QuotaField(view, [config[@"activityMinutes"] stringValue], @"Keepalive activity minutes", NSMakeRect(300, 192, 100, 26));
    QuotaLabel(view, @"Message", NSMakeRect(20, 240, 180, 22));
    self.message = [[NSTextView alloc] initWithFrame:NSMakeRect(0, 0, 672, 100)];
    self.message.richText = NO;
    self.message.allowsUndo = YES;
    self.message.font = [NSFont systemFontOfSize:NSFont.systemFontSize];
    self.message.textContainerInset = NSMakeSize(6, 6);
    self.message.autoresizingMask = NSViewWidthSizable;
    self.message.textContainer.widthTracksTextView = YES;
    self.message.horizontallyResizable = NO;
    self.message.verticallyResizable = YES;
    self.message.accessibilityLabel = @"Keepalive message";
    self.message.string = config[@"message"];
    QuotaScroll(view, self.message, NSMakeRect(20, 268, 694, 120));
}

- (void)refreshUpdateRefEnabled {
    BOOL specific = self.updateMode.indexOfSelectedItem == 2;
    self.updateRef.enabled = !self.applying && specific;
}

- (void)updateModeChanged:(id)sender {
    if (self.updateMode.indexOfSelectedItem == 0) self.updateRef.stringValue = @"";
    if (self.updateMode.indexOfSelectedItem == 1) self.updateRef.stringValue = @"main";
    [self refreshUpdateRefEnabled];
}

- (void)buildUpdate {
    NSView *view = [self addTab:@"Update"];
    NSDictionary *config = self.snapshot[@"update"];
    NSString *mode = config[@"mode"] ?: @"latest";
    NSString *ref = config[@"ref"] ?: @"";
    QuotaLabel(view, @"Install source", NSMakeRect(20, 24, 170, 22));
    self.updateMode = [[NSPopUpButton alloc] initWithFrame:NSMakeRect(210, 18, 260, 30) pullsDown:NO];
    [self.updateMode addItemsWithTitles:@[@"Latest release", @"Latest commit on main", @"Specific ref or commit"]];
    if ([mode isEqualToString:@"main"]) [self.updateMode selectItemAtIndex:1];
    else if ([mode isEqualToString:@"ref"]) [self.updateMode selectItemAtIndex:2];
    else [self.updateMode selectItemAtIndex:0];
    self.updateMode.target = self;
    self.updateMode.action = @selector(updateModeChanged:);
    self.updateMode.accessibilityLabel = @"Update install source";
    [view addSubview:self.updateMode];
    QuotaLabel(view, @"Ref", NSMakeRect(20, 72, 170, 22));
    self.updateRef = QuotaField(view, ref, @"Update ref", NSMakeRect(210, 68, 360, 26));
    self.updateRef.placeholderString = @"branch, tag, or commit";
    self.updateRef.toolTip = @"No spaces, control characters, or @.";
    NSTextField *hint = QuotaLabel(view, @"Used by quota-cli update and the menu bar update command.", NSMakeRect(20, 114, 690, 22));
    hint.textColor = NSColor.secondaryLabelColor;
    [self refreshUpdateRefEnabled];
}

- (void)showError:(NSString *)error {
    self.errorText.string = error;
    self.errorScroll.hidden = error.length == 0;
    [self.errorText scrollRangeToVisible:NSMakeRange(0, 0)];
}

- (void)openSnapshot:(NSDictionary *)snapshot token:(uint64_t)token {
    if (self.token != 0) {
        quotaSettingsClosed(token);
        QuotaShowSettingsWindowFront(self.window);
        return;
    }
    self.token = token;
    self.snapshot = snapshot;
    _applying = NO;
    NSView *content = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, 800, 620)];
    self.window.contentView = content;
    self.tabs = [[NSTabView alloc] initWithFrame:NSMakeRect(18, 126, 764, 476)];
    self.tabs.delegate = self;
    [content addSubview:self.tabs];
    [self buildGeneral];
    [self buildAccounts];
    [self buildKeepalive];
    [self buildUpdate];
    self.errorText = [[NSTextView alloc] initWithFrame:NSMakeRect(0, 0, 742, 54)];
    self.errorText.editable = NO;
    self.errorText.richText = NO;
    self.errorText.drawsBackground = NO;
    self.errorText.textColor = NSColor.systemRedColor;
    self.errorText.font = [NSFont systemFontOfSize:NSFont.smallSystemFontSize];
    self.errorText.autoresizingMask = NSViewWidthSizable;
    self.errorText.textContainer.widthTracksTextView = YES;
    self.errorText.accessibilityLabel = @"Settings error";
    self.errorScroll = QuotaScroll(content, self.errorText, NSMakeRect(26, 62, 748, 54));
    self.errorScroll.borderType = NSNoBorder;
    self.errorScroll.drawsBackground = NO;
    [self showError:@""];
    self.cancelButton = QuotaButton(content, @"Cancel", self, @selector(cancel:), NSMakeRect(574, 16, 96, 32));
    self.cancelButton.keyEquivalent = @"\e";
    self.saveButton = QuotaButton(content, @"Save", self, @selector(save:), NSMakeRect(682, 16, 96, 32));
    self.saveButton.keyEquivalent = @"\r";
    self.window.defaultButtonCell = self.saveButton.cell;
    [self.window standardWindowButton:NSWindowCloseButton].enabled = YES;
    self.window.initialFirstResponder = self.activeMinutes;
    [self.window recalculateKeyViewLoop];
    QuotaShowSettingsWindowFront(self.window);
    [self.window makeFirstResponder:self.activeMinutes];
}

- (void)tabView:(NSTabView *)tabView didSelectTabViewItem:(NSTabViewItem *)item {
    if (self.applying) return;
    NSView *first = nil;
    if ([item.identifier isEqual:@"General"]) first = self.activeMinutes;
    if ([item.identifier isEqual:@"Accounts"]) first = self.addAccount;
    if ([item.identifier isEqual:@"Keepalive"]) first = self.keepaliveEnabled;
    if ([item.identifier isEqual:@"Update"]) first = self.updateMode;
    if (first) [self.window makeFirstResponder:first];
}

- (void)addAccount:(id)sender {
    if (self.applying) return;
    QuotaSettingsAccountRow *row = [self appendAccount:@{@"provider": @"claude", @"key": @"", @"configDir": @"", @"minLeftPct": NSNull.null}];
    row.key.placeholderString = @"claude-2";
    [self layoutAccounts];
    [self.accountsDocument scrollRectToVisible:row.view.frame];
    [self.window makeFirstResponder:row.key];
}

- (void)removeAccount:(NSButton *)sender {
    if (self.applying) return;
    for (QuotaSettingsAccountRow *row in [self.accounts copy]) {
        if (row.remove == sender && !row.reserved) {
            [self.window makeFirstResponder:nil];
            [row.view removeFromSuperview];
            [self.accounts removeObject:row];
            [self layoutAccounts];
            [self.window makeFirstResponder:self.addAccount];
            break;
        }
    }
}

- (void)chooseDirectory:(NSButton *)sender {
    if (self.applying) return;
    for (QuotaSettingsAccountRow *row in self.accounts) {
        if (row.choose != sender || row.reserved) continue;
        NSOpenPanel *panel = [NSOpenPanel openPanel];
        panel.canChooseDirectories = YES;
        panel.canChooseFiles = NO;
        panel.allowsMultipleSelection = NO;
        panel.canCreateDirectories = NO;
        panel.showsHiddenFiles = YES;
        panel.prompt = @"Choose";
        panel.message = @"Choose an existing account config directory.";
        [panel beginSheetModalForWindow:self.window completionHandler:^(NSModalResponse result) {
            if (result == NSModalResponseOK && [self.accounts containsObject:row] && !self.applying) {
                row.directory.stringValue = panel.URL.path;
            }
        }];
        break;
    }
}

- (BOOL)fail:(NSString *)message tab:(NSUInteger)index field:(NSView *)field {
    [self.tabs selectTabViewItemAtIndex:index];
    [self showError:message];
    if (field) {
        [field scrollRectToVisible:field.bounds];
        [self.window makeFirstResponder:field];
    }
    return NO;
}

- (NSArray *)editedAccounts {
    NSMutableArray *edited = [NSMutableArray array];
    NSMutableSet *keys = [NSMutableSet set];
    NSMutableSet *directories = [NSMutableSet set];
    for (QuotaSettingsAccountRow *row in self.accounts) {
        NSString *provider = row.provider.titleOfSelectedItem;
        NSString *key = row.key.stringValue;
        NSString *directory = row.directory.stringValue;
        NSString *pattern = [NSString stringWithFormat:@"^%@-[0-9]+$", provider];
        NSRange keyMatch = [key rangeOfString:pattern options:NSRegularExpressionSearch];
        BOOL validKey = row.reserved ? [key isEqualToString:provider] : keyMatch.location == 0 && keyMatch.length == key.length;
        if (!validKey || [keys containsObject:key]) {
            [self fail:@"Account keys must be unique and match the provider (claude-2 or codex-2)." tab:1 field:row.key];
            return nil;
        }
        if (!row.reserved && [directory stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceAndNewlineCharacterSet].length == 0) {
            [self fail:@"Enter a config directory for each additional account." tab:1 field:row.directory];
            return nil;
        }
        NSString *directoryKey = [provider stringByAppendingFormat:@":%@", directory.stringByExpandingTildeInPath.stringByStandardizingPath];
        if (directory.length > 0 && [directories containsObject:directoryKey]) {
            [self fail:@"Each account for a provider needs a different config directory." tab:1 field:row.directory];
            return nil;
        }
        NSString *minimumText = [row.minimum.stringValue stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceAndNewlineCharacterSet];
        id minimum = NSNull.null;
        if (minimumText.length > 0) {
            NSScanner *scanner = [NSScanner scannerWithString:minimumText];
            scanner.locale = [NSLocale localeWithLocaleIdentifier:@"en_US_POSIX"];
            double value;
            if (![scanner scanDouble:&value] || !scanner.isAtEnd || !isfinite(value) || value < 0 || value > 100) {
                [self fail:@"Minimum remaining percent must be 0–100, or blank for the default (5%)." tab:1 field:row.minimum];
                return nil;
            }
            minimum = @(value);
        }
        [keys addObject:key];
        if (directory.length > 0) [directories addObject:directoryKey];
        [edited addObject:@{@"provider": provider, @"key": key, @"configDir": directory, @"minLeftPct": minimum}];
    }
    return edited;
}

- (void)setApplying:(BOOL)applying {
    _applying = applying;
    self.saveButton.enabled = !applying;
    self.saveButton.title = applying ? @"Saving…" : @"Save";
    self.cancelButton.enabled = !applying;
    [self.window standardWindowButton:NSWindowCloseButton].enabled = !applying;
    for (NSTabViewItem *item in self.tabs.tabViewItems) {
        NSMutableArray<NSView *> *pending = [NSMutableArray arrayWithObject:item.view];
        while (pending.count > 0) {
            NSView *view = pending.lastObject;
            [pending removeLastObject];
            if ([view isKindOfClass:[NSControl class]]) [(NSControl *)view setEnabled:!applying];
            [pending addObjectsFromArray:view.subviews];
        }
    }
    self.message.editable = !applying;
    [self refreshUpdateRefEnabled];
    for (QuotaSettingsAccountRow *row in self.accounts) {
        if (row.reserved) {
            row.provider.enabled = NO;
            row.key.enabled = NO;
            row.directory.enabled = NO;
            row.choose.enabled = NO;
            row.remove.enabled = NO;
        }
    }
}

- (void)save:(id)sender {
    if (self.applying || self.token == 0 || self.window.attachedSheet) return;
    if (![self.window makeFirstResponder:nil]) return;
    NSInteger active, idle, keepaliveIdle, activity;
    if (!QuotaInteger(self.activeMinutes, 1, INT64_MAX / 60000000000 - 5, &active)) {
        [self fail:@"Active refresh must be a positive whole number within the supported duration range." tab:0 field:self.activeMinutes];
        return;
    }
    if (!QuotaInteger(self.idleMinutes, 1, INT64_MAX / 60000000000 - 5, &idle)) {
        [self fail:@"Idle refresh must be a positive whole number within the supported duration range." tab:0 field:self.idleMinutes];
        return;
    }
    NSArray *accounts = [self editedAccounts];
    if (!accounts) return;
    NSMutableArray *days = [NSMutableArray array];
    for (NSButton *day in self.weekdays) if (day.state == NSControlStateValueOn) [days addObject:day.title];
    if (days.count == 0) {
        [self fail:@"Select at least one keepalive day." tab:2 field:self.weekdays.firstObject];
        return;
    }
    NSString *time = self.scheduledTime.stringValue;
    if ([time rangeOfString:@"^([01][0-9]|2[0-3]):[0-5][0-9]$" options:NSRegularExpressionSearch].location == NSNotFound || time.length != 5) {
        [self fail:@"Keepalive time must use 24-hour HH:MM format." tab:2 field:self.scheduledTime];
        return;
    }
    if (!QuotaInteger(self.keepaliveIdle, 1, 1440, &keepaliveIdle)) {
        [self fail:@"Keepalive idle minutes must be a whole number from 1 to 1440." tab:2 field:self.keepaliveIdle];
        return;
    }
    if (!QuotaInteger(self.activityMinutes, 1, 1440, &activity)) {
        [self fail:@"Keepalive activity minutes must be a whole number from 1 to 1440." tab:2 field:self.activityMinutes];
        return;
    }
    NSString *message = self.message.string;
    unichar zero = 0;
    if ([message stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceAndNewlineCharacterSet].length == 0 || [message lengthOfBytesUsingEncoding:NSUTF8StringEncoding] > 8192 || [message rangeOfString:[NSString stringWithCharacters:&zero length:1]].location != NSNotFound) {
        [self fail:@"Keepalive message must contain text and be at most 8192 UTF-8 bytes, without null characters." tab:2 field:self.message];
        return;
    }
    NSInteger updateIndex = self.updateMode.indexOfSelectedItem;
    NSString *updateMode = @[@"latest", @"main", @"ref"][updateIndex < 0 ? 0 : updateIndex];
    NSString *updateRef = self.updateRef.stringValue;
    if (updateIndex == 0) {
        updateRef = @"";
    } else if (updateIndex == 1) {
        updateRef = @"main";
    } else {
        NSString *trimmedRef = [updateRef stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceAndNewlineCharacterSet];
        NSMutableCharacterSet *bad = [[NSCharacterSet whitespaceAndNewlineCharacterSet] mutableCopy];
        [bad formUnionWithCharacterSet:NSCharacterSet.controlCharacterSet];
        if (trimmedRef.length == 0 || ![trimmedRef isEqualToString:updateRef] || [updateRef rangeOfString:@"@"].location != NSNotFound || [updateRef rangeOfCharacterFromSet:bad].location != NSNotFound) {
            [self fail:@"Update ref must be a branch, tag, or commit without spaces, control characters, or @." tab:3 field:self.updateRef];
            return;
        }
    }
    NSMutableDictionary *edited = [self.snapshot mutableCopy];
    edited[@"refreshActiveMinutes"] = @(active);
    edited[@"refreshIdleMinutes"] = @(idle);
    edited[@"showResetTime"] = [NSNumber numberWithBool:self.showReset.state == NSControlStateValueOn];
    edited[@"startAtLogin"] = [NSNumber numberWithBool:self.startAtLogin.state == NSControlStateValueOn];
    edited[@"accounts"] = accounts;
    NSArray *items = self.snapshot[@"availableItems"];
    NSMutableArray *selected = [self.snapshot[@"selected"] mutableCopy];
    [items enumerateObjectsUsingBlock:^(NSDictionary *item, NSUInteger index, BOOL * __unused stop) {
        NSString *key = item[@"key"];
        BOOL checked = self.displayChecks[index].state == NSControlStateValueOn;
        if (!checked) [selected removeObject:key];
        else if (![selected containsObject:key]) [selected addObject:key];
    }];
    edited[@"selected"] = selected;
    edited[@"keepalive"] = @{@"enabled": [NSNumber numberWithBool:self.keepaliveEnabled.state == NSControlStateValueOn], @"weekdays": days, @"time": time, @"idleMinutes": @(keepaliveIdle), @"activityMinutes": @(activity), @"message": message};
    edited[@"update"] = @{@"mode": updateMode, @"ref": updateRef};
    NSError *error;
    NSData *data = [NSJSONSerialization dataWithJSONObject:edited options:0 error:&error];
    if (!data) {
        [self showError:error.localizedDescription];
        return;
    }
    [self showError:@""];
    self.applying = YES;
    NSString *json = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
    quotaSettingsApply(self.token, (char *)json.UTF8String);
}

- (void)complete:(NSString *)error token:(uint64_t)token {
    if (token != self.token || !self.applying) return;
    self.applying = NO;
    if (error.length > 0) {
        [self showError:error];
        [self.window makeFirstResponder:self.saveButton];
    } else {
        [self.window close];
    }
}

- (void)cancel:(id)sender {
    if (!self.applying) [self.window performClose:sender];
}

- (BOOL)windowShouldClose:(NSWindow *)sender {
    return !self.applying && sender.attachedSheet == nil;
}

- (void)windowWillClose:(NSNotification *)notification {
    uint64_t token = self.token;
    self.token = 0;
    self.snapshot = nil;
    [self.window endEditingFor:nil];
    [self.window.undoManager removeAllActions];
    if (token != 0) quotaSettingsClosed(token);
}

@end

static QuotaSettingsController *quotaSettingsController;

void quota_settings_open(uint64_t token, const char *snapshot_json) {
    @autoreleasepool {
        NSString *json = [NSString stringWithUTF8String:snapshot_json];
        dispatch_async(dispatch_get_main_queue(), ^{
            NSDictionary *snapshot = [NSJSONSerialization JSONObjectWithData:[json dataUsingEncoding:NSUTF8StringEncoding] options:0 error:nil];
            if (NSApp.activationPolicy == NSApplicationActivationPolicyProhibited) {
                [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
            }
            if (!quotaSettingsController) quotaSettingsController = [[QuotaSettingsController alloc] init];
            [quotaSettingsController openSnapshot:snapshot token:token];
        });
    }
}

void quota_settings_complete(uint64_t token, const char *error_message) {
    @autoreleasepool {
        NSString *error = [NSString stringWithUTF8String:error_message];
        dispatch_async(dispatch_get_main_queue(), ^{
            [quotaSettingsController complete:error token:token];
        });
    }
}
