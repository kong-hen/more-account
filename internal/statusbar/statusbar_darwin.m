//go:build darwin

#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include <stdatomic.h>
#include <stdlib.h>
#import "statusbar_darwin.h"

// 确保 AppKit 操作始终运行在主线程。
static inline void MARunOnMainThread(dispatch_block_t block) {
    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_async(dispatch_get_main_queue(), block);
    }
}

// ─── 窗口关闭拦截与 Dock 重开钩子 ───────────────────────────────

static atomic_bool gCloseToBackground = true;
static BOOL gWindowHookInstalled = NO;
static BOOL gAppHookInstalled = NO;
static IMP gOrigWindowShouldClose = NULL;
static IMP gOrigAppShouldHandleReopen = NULL;

static BOOL MAWindowShouldCloseIMP(id self, SEL _cmd, id sender) {
    if (atomic_load(&gCloseToBackground)) {
        if ([sender isKindOfClass:[NSWindow class]]) {
            [(NSWindow *)sender orderOut:nil];
        }
        return NO;
    }
    if (gOrigWindowShouldClose != NULL) {
        typedef BOOL (*OrigCloseFn)(id, SEL, id);
        return ((OrigCloseFn)gOrigWindowShouldClose)(self, _cmd, sender);
    }
    return YES;
}

static BOOL MAAppShouldHandleReopenIMP(id self, SEL _cmd, NSApplication *sender, BOOL hasVisibleWindows) {
    if (!hasVisibleWindows) {
        maStatusBarOnShowWindow();
    }
    if (gOrigAppShouldHandleReopen != NULL) {
        typedef BOOL (*OrigReopenFn)(id, SEL, NSApplication *, BOOL);
        return ((OrigReopenFn)gOrigAppShouldHandleReopen)(self, _cmd, sender, hasVisibleWindows);
    }
    return YES;
}

static void MATryInstallWindowHooks(int attemptsLeft) {
    NSApplication *app = [NSApplication sharedApplication];

    // 1. 包装主窗口 delegate 的 windowShouldClose:（仅作用于窗口红叉与 performClose，不拦截 Cmd+Q）
    if (!gWindowHookInstalled && app != nil) {
        NSArray<NSWindow *> *windows = [app windows];
        for (NSWindow *win in windows) {
            id del = [win delegate];
            if (del != nil && [del respondsToSelector:@selector(windowShouldClose:)]) {
                Class delCls = object_getClass(del);
                Method m = class_getInstanceMethod(delCls, @selector(windowShouldClose:));
                if (m != NULL) {
                    gOrigWindowShouldClose = method_setImplementation(m, (IMP)MAWindowShouldCloseIMP);
                    gWindowHookInstalled = YES;
                    break;
                }
            }
        }
    }

    // 2. 为 NSApp.delegate 补齐 applicationShouldHandleReopen:hasVisibleWindows:（点击 Dock 恢复隐藏窗口）
    if (!gAppHookInstalled && app != nil) {
        id appDel = [app delegate];
        if (appDel != nil) {
            Class appDelCls = object_getClass(appDel);
            SEL reopenSel = @selector(applicationShouldHandleReopen:hasVisibleWindows:);
            Method existing = class_getInstanceMethod(appDelCls, reopenSel);
            if (existing != NULL) {
                gOrigAppShouldHandleReopen = method_setImplementation(existing, (IMP)MAAppShouldHandleReopenIMP);
                gAppHookInstalled = YES;
            } else {
                // ObjC 类型编码："c@:@c" 对应 BOOL(id, SEL, id, BOOL)
                if (class_addMethod(appDelCls, reopenSel, (IMP)MAAppShouldHandleReopenIMP, "c@:@c")) {
                    gAppHookInstalled = YES;
                }
            }
        }
    }

    // Wails OnStartup 并发于主循环启动，若窗口或 delegate 尚未挂载则短暂重试
    if ((!gWindowHookInstalled || !gAppHookInstalled) && attemptsLeft > 0) {
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(50 * NSEC_PER_MSEC)),
                       dispatch_get_main_queue(), ^{
            MATryInstallWindowHooks(attemptsLeft - 1);
        });
    }
}

// ─── macOS 顶部状态栏控制器 ─────────────────────────────────────

@interface MAStatusBarController : NSObject <NSMenuDelegate>
@property (nonatomic, strong) NSStatusItem *statusItem;
@property (nonatomic, strong) NSMenu *menu;
@property (nonatomic, strong) NSImage *templateIcon;
@property (nonatomic, strong) NSDictionary *lastSnapshot;
@property (nonatomic, assign) BOOL stopped;
- (void)applySnapshotJSON:(NSString *)jsonString;
- (void)stop;
@end

static MAStatusBarController *gController = nil;

@implementation MAStatusBarController

- (instancetype)initWithIconData:(NSData *)iconData {
    self = [super init];
    if (self) {
        if (iconData.length > 0) {
            NSImage *img = [[NSImage alloc] initWithData:iconData];
            if (img != nil) {
                [img setSize:NSMakeSize(18.0, 18.0)];
                [img setTemplate:YES];
                self.templateIcon = img;
            }
        }
        self.menu = [[NSMenu alloc] initWithTitle:@"More Account"];
        self.menu.autoenablesItems = NO;
        self.menu.delegate = self;
    }
    return self;
}

- (NSDictionary *)parseSnapshotJSON:(NSString *)jsonString {
    if (jsonString.length == 0) {
        return nil;
    }
    NSData *data = [jsonString dataUsingEncoding:NSUTF8StringEncoding];
    if (data == nil) {
        return nil;
    }
    NSError *err = nil;
    id obj = [NSJSONSerialization JSONObjectWithData:data options:0 error:&err];
    if (err != nil || ![obj isKindOfClass:[NSDictionary class]]) {
        return nil;
    }
    return (NSDictionary *)obj;
}

- (void)ensureStatusItem {
    if (self.statusItem != nil) {
        return;
    }
    NSStatusItem *item = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
    item.menu = self.menu;
    NSStatusBarButton *btn = item.button;
    if (btn != nil && self.templateIcon != nil) {
        btn.image = self.templateIcon;
        btn.imagePosition = NSImageOnly;
    }
    self.statusItem = item;
}

- (void)removeStatusItem {
    if (self.statusItem != nil) {
        self.statusItem.menu = nil;
        [[NSStatusBar systemStatusBar] removeStatusItem:self.statusItem];
        self.statusItem = nil;
    }
}

- (void)updateButtonWithSnapshot:(NSDictionary *)snap {
    NSStatusBarButton *btn = self.statusItem.button;
    if (btn == nil) {
        return;
    }
    if (self.templateIcon != nil) {
        btn.image = self.templateIcon;
    }

    NSString *title = [snap[@"buttonTitle"] isKindOfClass:[NSString class]] ? snap[@"buttonTitle"] : @"";
    if (title.length > 0) {
        NSFont *monoFont = [NSFont monospacedDigitSystemFontOfSize:12.0 weight:NSFontWeightMedium];
        NSDictionary *attrs = @{
            NSFontAttributeName: monoFont,
        };
        NSString *spaced = [@" " stringByAppendingString:title];
        btn.attributedTitle = [[NSAttributedString alloc] initWithString:spaced attributes:attrs];
        btn.imagePosition = NSImageLeft;
    } else {
        btn.attributedTitle = [[NSAttributedString alloc] initWithString:@""];
        btn.title = @"";
        btn.imagePosition = NSImageOnly;
    }

    NSString *tooltip = [snap[@"tooltip"] isKindOfClass:[NSString class]] ? snap[@"tooltip"] : @"More Account";
    btn.toolTip = tooltip.length > 0 ? tooltip : @"More Account";
}

- (NSMenuItem *)makeInfoItem:(NSString *)title bold:(BOOL)bold monoDigits:(BOOL)monoDigits {
    NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:title action:nil keyEquivalent:@""];
    item.enabled = NO;

    NSFont *font = nil;
    if (monoDigits) {
        font = [NSFont monospacedDigitSystemFontOfSize:12.0 weight:(bold ? NSFontWeightSemibold : NSFontWeightRegular)];
    } else {
        font = [NSFont systemFontOfSize:12.5 weight:(bold ? NSFontWeightSemibold : NSFontWeightRegular)];
    }
    NSColor *color = bold ? [NSColor labelColor] : [NSColor secondaryLabelColor];
    item.attributedTitle = [[NSAttributedString alloc] initWithString:title attributes:@{
        NSFontAttributeName: font,
        NSForegroundColorAttributeName: color,
    }];
    return item;
}

- (NSMenuItem *)makeErrorItem:(NSString *)title {
    NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:title action:nil keyEquivalent:@""];
    item.enabled = NO;
    item.attributedTitle = [[NSAttributedString alloc] initWithString:title attributes:@{
        NSFontAttributeName: [NSFont systemFontOfSize:12.0 weight:NSFontWeightMedium],
        NSForegroundColorAttributeName: [NSColor systemRedColor],
    }];
    return item;
}

- (void)rebuildMenuWithSnapshot:(NSDictionary *)snap {
    [self.menu removeAllItems];
    if (snap == nil) {
        return;
    }

    // 1. 当前账号与核心配额摘要区
    NSString *header = [snap[@"currentHeader"] isKindOfClass:[NSString class]] ? snap[@"currentHeader"] : @"More Account";
    [self.menu addItem:[self makeInfoItem:header bold:YES monoDigits:NO]];

    NSString *statusHint = [snap[@"statusHint"] isKindOfClass:[NSString class]] ? snap[@"statusHint"] : @"";
    if (statusHint.length > 0) {
        [self.menu addItem:[self makeInfoItem:[@"  " stringByAppendingString:statusHint] bold:NO monoDigits:NO]];
    }

    NSString *errorBanner = [snap[@"errorBanner"] isKindOfClass:[NSString class]] ? snap[@"errorBanner"] : @"";
    if (errorBanner.length > 0) {
        [self.menu addItem:[self makeErrorItem:errorBanner]];
    }

    NSArray *categories = [snap[@"categories"] isKindOfClass:[NSArray class]] ? snap[@"categories"] : @[];
    if (categories.count > 0) {
        for (id rawCat in categories) {
            if (![rawCat isKindOfClass:[NSDictionary class]]) {
                continue;
            }
            NSString *line = rawCat[@"text"];
            if ([line isKindOfClass:[NSString class]] && line.length > 0) {
                [self.menu addItem:[self makeInfoItem:line bold:NO monoDigits:YES]];
            }
        }
    } else {
        NSString *emptyHint = [snap[@"quotaEmptyHint"] isKindOfClass:[NSString class]] ? snap[@"quotaEmptyHint"] : @"";
        if (emptyHint.length > 0) {
            [self.menu addItem:[self makeInfoItem:emptyHint bold:NO monoDigits:NO]];
        }
    }

    [self.menu addItem:[NSMenuItem separatorItem]];

    // 2. 快速切换账号二级子菜单
    NSMenuItem *switchRoot = [[NSMenuItem alloc] initWithTitle:@"快速切换账号" action:nil keyEquivalent:@""];
    NSMenu *switchSubmenu = [[NSMenu alloc] initWithTitle:@"快速切换账号"];
    switchSubmenu.autoenablesItems = NO;

    NSArray *accounts = [snap[@"accounts"] isKindOfClass:[NSArray class]] ? snap[@"accounts"] : @[];
    if (accounts.count == 0) {
        NSMenuItem *noneItem = [[NSMenuItem alloc] initWithTitle:@"暂无账号" action:nil keyEquivalent:@""];
        noneItem.enabled = NO;
        [switchSubmenu addItem:noneItem];
    } else {
        NSFont *accFont = [NSFont monospacedDigitSystemFontOfSize:13.0 weight:NSFontWeightRegular];
        for (id rawAcc in accounts) {
            if (![rawAcc isKindOfClass:[NSDictionary class]]) {
                continue;
            }
            NSString *accID = [rawAcc[@"id"] isKindOfClass:[NSString class]] ? rawAcc[@"id"] : @"";
            NSString *accTitle = [rawAcc[@"title"] isKindOfClass:[NSString class]] ? rawAcc[@"title"] : @"";
            BOOL isActive = [rawAcc[@"active"] boolValue];
            BOOL isDisabled = [rawAcc[@"disabled"] boolValue];

            NSMenuItem *accItem = [[NSMenuItem alloc] initWithTitle:accTitle
                                                             action:@selector(handleSwitchAccount:)
                                                      keyEquivalent:@""];
            accItem.target = self;
            accItem.representedObject = @{ @"id": accID, @"active": @(isActive) };
            accItem.state = isActive ? NSControlStateValueOn : NSControlStateValueOff;
            accItem.enabled = !isDisabled;
            accItem.attributedTitle = [[NSAttributedString alloc] initWithString:accTitle attributes:@{
                NSFontAttributeName: accFont,
            }];
            [switchSubmenu addItem:accItem];
        }
    }
    switchRoot.submenu = switchSubmenu;
    switchRoot.enabled = YES;
    [self.menu addItem:switchRoot];

    [self.menu addItem:[NSMenuItem separatorItem]];

    // 3. 快捷操作区
    BOOL refreshing = [snap[@"refreshing"] boolValue];
    BOOL canRefreshCurrent = [snap[@"canRefreshCurrent"] boolValue];
    BOOL canRefreshAll = [snap[@"canRefreshAll"] boolValue];
    BOOL privacyMode = [snap[@"privacyMode"] boolValue];

    NSString *refreshCurTitle = refreshing ? @"正在刷新配额..." : @"刷新当前账号配额";
    NSMenuItem *refreshCurItem = [[NSMenuItem alloc] initWithTitle:refreshCurTitle
                                                            action:@selector(handleRefreshCurrent:)
                                                     keyEquivalent:@""];
    refreshCurItem.target = self;
    refreshCurItem.enabled = canRefreshCurrent;
    [self.menu addItem:refreshCurItem];

    NSMenuItem *refreshAllItem = [[NSMenuItem alloc] initWithTitle:@"刷新全部账号配额"
                                                            action:@selector(handleRefreshAll:)
                                                     keyEquivalent:@""];
    refreshAllItem.target = self;
    refreshAllItem.enabled = canRefreshAll;
    [self.menu addItem:refreshAllItem];

    NSMenuItem *privacyItem = [[NSMenuItem alloc] initWithTitle:@"隐私脱敏模式"
                                                         action:@selector(handleTogglePrivacy:)
                                                  keyEquivalent:@""];
    privacyItem.target = self;
    privacyItem.state = privacyMode ? NSControlStateValueOn : NSControlStateValueOff;
    privacyItem.enabled = YES;
    [self.menu addItem:privacyItem];

    [self.menu addItem:[NSMenuItem separatorItem]];

    // 4. 窗口与退出控制区
    NSMenuItem *showItem = [[NSMenuItem alloc] initWithTitle:@"打开主窗口"
                                                      action:@selector(handleShowWindow:)
                                               keyEquivalent:@""];
    showItem.target = self;
    showItem.enabled = YES;
    [self.menu addItem:showItem];

    NSMenuItem *quitItem = [[NSMenuItem alloc] initWithTitle:@"退出 More Account"
                                                      action:@selector(handleQuit:)
                                               keyEquivalent:@"q"];
    quitItem.keyEquivalentModifierMask = NSEventModifierFlagCommand;
    quitItem.target = self;
    quitItem.enabled = YES;
    [self.menu addItem:quitItem];
}

- (void)applySnapshotJSON:(NSString *)jsonString {
    if (self.stopped) {
        return;
    }
    NSDictionary *snap = [self parseSnapshotJSON:jsonString];
    if (snap == nil) {
        return;
    }
    self.lastSnapshot = snap;

    BOOL visible = [snap[@"visible"] boolValue];
    if (!visible) {
        [self removeStatusItem];
        return;
    }
    [self ensureStatusItem];
    [self updateButtonWithSnapshot:snap];
    [self rebuildMenuWithSnapshot:snap];
}

- (void)menuWillOpen:(NSMenu *)menu {
    if (self.stopped) {
        return;
    }
    // 展开菜单前在主线程同步向 Go 内存 Store 拉取最新快照（无网络 I/O、不回派主线程），保证重置倒计时实时精确
    char *freshJSON = maStatusBarFetchSnapshotJSON();
    if (freshJSON != NULL) {
        NSString *nsJSON = [NSString stringWithUTF8String:freshJSON];
        free(freshJSON);
        NSDictionary *snap = [self parseSnapshotJSON:nsJSON];
        if (snap != nil) {
            self.lastSnapshot = snap;
            [self updateButtonWithSnapshot:snap];
            [self rebuildMenuWithSnapshot:snap];
        }
    }
}

- (void)handleSwitchAccount:(NSMenuItem *)sender {
    if (self.stopped || ![sender.representedObject isKindOfClass:[NSDictionary class]]) {
        return;
    }
    NSDictionary *payload = (NSDictionary *)sender.representedObject;
    if ([payload[@"active"] boolValue]) {
        return;
    }
    NSString *accID = payload[@"id"];
    if (![accID isKindOfClass:[NSString class]] || accID.length == 0) {
        return;
    }
    const char *utf8 = [accID UTF8String];
    if (utf8 != NULL) {
        maStatusBarOnSwitchAccount((char *)utf8);
    }
}

- (void)handleRefreshCurrent:(id)sender {
    if (!self.stopped) {
        maStatusBarOnRefreshCurrent();
    }
}

- (void)handleRefreshAll:(id)sender {
    if (!self.stopped) {
        maStatusBarOnRefreshAll();
    }
}

- (void)handleTogglePrivacy:(id)sender {
    if (!self.stopped) {
        maStatusBarOnTogglePrivacy();
    }
}

- (void)handleShowWindow:(id)sender {
    if (!self.stopped) {
        maStatusBarOnShowWindow();
    }
}

- (void)handleQuit:(id)sender {
    if (!self.stopped) {
        maStatusBarOnQuit();
    }
}

- (void)stop {
    self.stopped = YES;
    self.menu.delegate = nil;
    [self removeStatusItem];
}

@end

// ─── 导出 C 入口 ───────────────────────────────────────────────

void MAStatusBarStart(const void *iconBytes, int iconLen, bool closeToBackground) {
    atomic_store(&gCloseToBackground, closeToBackground);
    NSData *iconData = nil;
    if (iconBytes != NULL && iconLen > 0) {
        iconData = [NSData dataWithBytes:iconBytes length:(NSUInteger)iconLen];
    }
    MARunOnMainThread(^{
        MATryInstallWindowHooks(20);
        if (gController == nil) {
            gController = [[MAStatusBarController alloc] initWithIconData:iconData];
        } else {
            gController.stopped = NO;
        }
    });
}

void MAStatusBarSetCloseToBackground(bool enabled) {
    atomic_store(&gCloseToBackground, enabled);
}

void MAStatusBarUpdateJSON(const char *snapshotJSON) {
    if (snapshotJSON == NULL) {
        return;
    }
    NSString *copy = [NSString stringWithUTF8String:snapshotJSON];
    if (copy == nil) {
        return;
    }
    MARunOnMainThread(^{
        if (gController != nil && !gController.stopped) {
            [gController applySnapshotJSON:copy];
        }
    });
}

void MAStatusBarStop(void) {
    MARunOnMainThread(^{
        if (gController != nil) {
            [gController stop];
            gController = nil;
        }
    });
}
