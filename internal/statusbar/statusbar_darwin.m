//go:build darwin

#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include <stdatomic.h>
#include <stdio.h>
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
static atomic_bool gShuttingDown = false;
static BOOL gWindowHookInstalled = NO;
static BOOL gAppHookInstalled = NO;
static IMP gOrigWindowShouldClose = NULL;
static IMP gOrigAppShouldHandleReopen = NULL;
static __weak NSWindow *gMainWindow = nil;
static id gLaunchObserver = nil;

static void MATryInstallWindowHooks(int attemptsLeft);

static BOOL MAIsAuxiliaryWindow(NSWindow *win) {
    if (win == nil) {
        return YES;
    }
    NSString *clsName = NSStringFromClass([win class]);
    if ([clsName isEqualToString:@"NSStatusBarWindow"] ||
        [clsName hasPrefix:@"TUINS"] ||
        [clsName isEqualToString:@"NSCarbonMenuWindow"] ||
        [clsName isEqualToString:@"NSPopupMenuWindow"]) {
        return YES;
    }
    if ([win isKindOfClass:[NSPanel class]]) {
        return YES;
    }
    return NO;
}

static NSWindow *MAFindMainWindow(NSApplication *app) {
    NSWindow *cached = gMainWindow;
    if (cached != nil) {
        return cached;
    }
    if (app == nil) {
        return nil;
    }
    // 1. 优先直接匹配 Wails 主窗口类 WailsWindow
    Class wailsWinCls = NSClassFromString(@"WailsWindow");
    NSArray<NSWindow *> *windows = [app windows];
    for (NSWindow *win in windows) {
        if (wailsWinCls != Nil && [win isKindOfClass:wailsWinCls]) {
            gMainWindow = win;
            return win;
        }
    }
    // 2. 次优从 Wails AppDelegate.mainWindow 属性获取主窗口引用
    id appDel = [app delegate];
    SEL mainWinSel = NSSelectorFromString(@"mainWindow");
    if (appDel != nil && [appDel respondsToSelector:mainWinSel]) {
        typedef id (*MainWinGetter)(id, SEL);
        id candidate = ((MainWinGetter)[appDel methodForSelector:mainWinSel])(appDel, mainWinSel);
        if ([candidate isKindOfClass:[NSWindow class]] && !MAIsAuxiliaryWindow((NSWindow *)candidate)) {
            gMainWindow = (NSWindow *)candidate;
            return (NSWindow *)candidate;
        }
    }
    // 3. 兜底匹配普通层级且具备关闭能力的非辅助窗口（兼容单元测试场景）
    for (NSWindow *win in windows) {
        if (MAIsAuxiliaryWindow(win)) {
            continue;
        }
        if ([win level] == NSNormalWindowLevel &&
            (([win styleMask] & NSWindowStyleMaskClosable) != 0 ||
             [[win delegate] respondsToSelector:@selector(windowShouldClose:)])) {
            gMainWindow = win;
            return win;
        }
    }
    return nil;
}

static void MAShowMainWindowOnMainThread(void) {
    NSApplication *app = [NSApplication sharedApplication];
    if (app == nil) {
        return;
    }
    // 兜底确保窗口关闭拦截与 Dock 重开钩子均已安装
    if (!gWindowHookInstalled || !gAppHookInstalled) {
        MATryInstallWindowHooks(0);
    }
    // 若应用此前通过 Cmd+H 整体隐藏，须先解除隐藏态再操作窗口可见性与前置顺序
    if ([app isHidden]) {
        [app unhide:nil];
    }
    NSWindow *win = MAFindMainWindow(app);
    if (win != nil) {
        if ([win isMiniaturized]) {
            [win deminiaturize:nil];
        }
        [win makeKeyAndOrderFront:nil];
    }
    // 必须使用 activateIgnoringOtherApps:YES 才能在后台跨越其他前台应用恢复焦点；
    // macOS 14+ 上仅调用 [NSApp activate] 在无合作激活令牌时会被系统静默忽略。
    [app activateIgnoringOtherApps:YES];
    if (win != nil) {
        [win makeKeyAndOrderFront:nil];
    }
    NSWindow *modalWin = [app modalWindow];
    if (modalWin != nil && modalWin != win) {
        [modalWin makeKeyAndOrderFront:nil];
    }
}

static BOOL MAWindowShouldCloseIMP(id self, SEL _cmd, id sender) {
    if (atomic_load(&gCloseToBackground)) {
        if ([sender isKindOfClass:[NSWindow class]]) {
            NSWindow *win = (NSWindow *)sender;
            if (gMainWindow == nil && !MAIsAuxiliaryWindow(win)) {
                gMainWindow = win;
            }
            [win orderOut:nil];
        }
        return NO;
    }
    if (gOrigWindowShouldClose != NULL && gOrigWindowShouldClose != (IMP)MAWindowShouldCloseIMP) {
        typedef BOOL (*OrigCloseFn)(id, SEL, id);
        return ((OrigCloseFn)gOrigWindowShouldClose)(self, _cmd, sender);
    }
    return YES;
}

static BOOL MAAppShouldHandleReopenIMP(id self, SEL _cmd, NSApplication *sender, BOOL hasVisibleWindows) {
    if (!atomic_load(&gShuttingDown)) {
        // 绝不使用 `if (!hasVisibleWindows)` 守卫：
        // 1) 当存在状态栏图标、辅助窗口、最小化窗口或应用被 Cmd+H 隐藏时，hasVisibleWindows 可能为 YES；
        // 2) 只要用户点击 Dock 图标触发 reopen，就必须在主线程同步恢复并前置主窗口，再通知 Go 层刷新业务状态。
        MAShowMainWindowOnMainThread();
        maStatusBarOnShowWindow();
    }
    if (gOrigAppShouldHandleReopen != NULL && gOrigAppShouldHandleReopen != (IMP)MAAppShouldHandleReopenIMP) {
        typedef BOOL (*OrigReopenFn)(id, SEL, NSApplication *, BOOL);
        return ((OrigReopenFn)gOrigAppShouldHandleReopen)(self, _cmd, sender, hasVisibleWindows);
    }
    // 已由当前钩子同步完成主窗口恢复与激活，返回 NO 阻止 AppKit 再执行冗余的 _doOpenUntitled 等默认动作；
    // AppKit 仍会在返回后继续执行 _switchToSpaceIfNeeded 切回窗口所在桌面空间。
    return NO;
}

static void MATryInstallWindowHooks(int attemptsLeft) {
    NSApplication *app = [NSApplication sharedApplication];

    // 1. 包装主窗口 delegate 的 windowShouldClose:（仅作用于窗口红叉与 performClose，不拦截 Cmd+Q）
    if (!gWindowHookInstalled && app != nil) {
        NSWindow *win = MAFindMainWindow(app);
        id del = [win delegate];
        if (del != nil && [del respondsToSelector:@selector(windowShouldClose:)]) {
            Class delCls = object_getClass(del);
            SEL closeSel = @selector(windowShouldClose:);
            Method m = class_getInstanceMethod(delCls, closeSel);
            if (m != NULL) {
                IMP currentIMP = method_getImplementation(m);
                if (currentIMP == (IMP)MAWindowShouldCloseIMP) {
                    gWindowHookInstalled = YES;
                } else {
                    const char *types = method_getTypeEncoding(m);
                    gOrigWindowShouldClose = currentIMP;
                    class_replaceMethod(delCls, closeSel, (IMP)MAWindowShouldCloseIMP, types != NULL ? types : "c@:@");
                    gWindowHookInstalled = YES;
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
                IMP currentIMP = method_getImplementation(existing);
                if (currentIMP == (IMP)MAAppShouldHandleReopenIMP) {
                    gAppHookInstalled = YES;
                } else {
                    const char *types = method_getTypeEncoding(existing);
                    gOrigAppShouldHandleReopen = currentIMP;
                    class_replaceMethod(appDelCls, reopenSel, (IMP)MAAppShouldHandleReopenIMP, types != NULL ? types : "c@:@c");
                    gAppHookInstalled = YES;
                }
            } else {
                char typeEnc[16];
                snprintf(typeEnc, sizeof(typeEnc), "%s@:@%s", @encode(BOOL), @encode(BOOL));
                if (class_addMethod(appDelCls, reopenSel, (IMP)MAAppShouldHandleReopenIMP, typeEnc)) {
                    gAppHookInstalled = YES;
                    // AppKit 在 setDelegate: 时可能缓存 delegate 方法响应位与通知表；
                    // 在 setDelegate: 之后动态注入方法，须持强引用先清空再重设以强制刷新 AppKit 内部缓存。
                    id strongDelegate = appDel;
                    [app setDelegate:nil];
                    [app setDelegate:strongDelegate];
                }
            }
        }
    }

    if (gWindowHookInstalled && gAppHookInstalled) {
        if (gLaunchObserver != nil) {
            [[NSNotificationCenter defaultCenter] removeObserver:gLaunchObserver];
            gLaunchObserver = nil;
        }
        return;
    }

    // 注册应用启动完成通知作为兜底，防止启动较慢时错过重试窗口
    if (gLaunchObserver == nil && app != nil) {
        gLaunchObserver = [[NSNotificationCenter defaultCenter]
            addObserverForName:NSApplicationDidFinishLaunchingNotification
                        object:app
                         queue:[NSOperationQueue mainQueue]
                    usingBlock:^(NSNotification * _Nonnull note) {
            (void)note;
            if (!atomic_load(&gShuttingDown)) {
                MATryInstallWindowHooks(0);
            }
        }];
    }

    // Wails OnStartup 并发于主循环启动，若窗口或 delegate 尚未挂载则短暂重试
    if (attemptsLeft > 0) {
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(50 * NSEC_PER_MSEC)),
                       dispatch_get_main_queue(), ^{
            if (!atomic_load(&gShuttingDown)) {
                MATryInstallWindowHooks(attemptsLeft - 1);
            }
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
@property (nonatomic, assign) BOOL menuOpen;
@property (nonatomic, assign) BOOL pendingMenuRebuild;
- (void)applySnapshotJSON:(NSString *)jsonString;
- (void)stop;
@end

static MAStatusBarController *gController = nil;
static id gWakeObserver = nil;
static id gAppNapActivity = nil;

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
        self.pendingMenuRebuild = NO;
        [self removeStatusItem];
        return;
    }
    [self ensureStatusItem];
    [self updateButtonWithSnapshot:snap];
    // 菜单处于展开追踪期时禁止调用 removeAllItems，避免正在操作的菜单或二级子菜单突然关闭/闪断；
    // 仅缓存最新快照并标记待重建，待 menuDidClose: 时平滑刷新。
    if (self.menuOpen) {
        self.pendingMenuRebuild = YES;
        return;
    }
    self.pendingMenuRebuild = NO;
    [self rebuildMenuWithSnapshot:snap];
}

- (void)menuWillOpen:(NSMenu *)menu {
    if (self.stopped) {
        return;
    }
    self.menuOpen = YES;
    self.pendingMenuRebuild = NO;
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

- (void)menuDidClose:(NSMenu *)menu {
    self.menuOpen = NO;
    if (self.stopped) {
        self.pendingMenuRebuild = NO;
        return;
    }
    if (self.pendingMenuRebuild && self.lastSnapshot != nil) {
        self.pendingMenuRebuild = NO;
        [self rebuildMenuWithSnapshot:self.lastSnapshot];
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
    atomic_store(&gShuttingDown, false);
    NSData *iconData = nil;
    if (iconBytes != NULL && iconLen > 0) {
        iconData = [NSData dataWithBytes:iconBytes length:(NSUInteger)iconLen];
    }
    MARunOnMainThread(^{
        MATryInstallWindowHooks(60);
        if (gController == nil) {
            gController = [[MAStatusBarController alloc] initWithIconData:iconData];
        } else {
            gController.stopped = NO;
        }
        // 注册系统休眠唤醒通知，使 Go 侧定时唤醒、配额刷新与账号探测在机器醒来后即时补检
        if (gWakeObserver == nil) {
            NSNotificationCenter *wsCenter = [[NSWorkspace sharedWorkspace] notificationCenter];
            gWakeObserver = [wsCenter addObserverForName:NSWorkspaceDidWakeNotification
                                                  object:nil
                                                   queue:[NSOperationQueue mainQueue]
                                              usingBlock:^(NSNotification * _Nonnull note) {
                (void)note;
                maStatusBarOnSystemWake();
            }];
        }
    });
}

void MAStatusBarSetCloseToBackground(bool enabled) {
    atomic_store(&gCloseToBackground, enabled);
}

void MAStatusBarSetPreventAppNap(bool enabled) {
    MARunOnMainThread(^{
        if (enabled) {
            if (gAppNapActivity == nil) {
                // 使用 NSActivityUserInitiatedAllowingIdleSystemSleep 抑制后台 App Nap 定时器合并，同时允许系统正常空闲休眠
                gAppNapActivity = [[NSProcessInfo processInfo]
                    beginActivityWithOptions:NSActivityUserInitiatedAllowingIdleSystemSleep
                                      reason:@"More Account background scheduled wakeup and quota refresh"];
            }
        } else {
            if (gAppNapActivity != nil) {
                [[NSProcessInfo processInfo] endActivity:gAppNapActivity];
                gAppNapActivity = nil;
            }
        }
    });
}

void MAStatusBarShowMainWindow(void) {
    if (atomic_load(&gShuttingDown)) {
        return;
    }
    MARunOnMainThread(^{
        if (!atomic_load(&gShuttingDown)) {
            MAShowMainWindowOnMainThread();
        }
    });
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
    atomic_store(&gShuttingDown, true);
    MARunOnMainThread(^{
        if (gLaunchObserver != nil) {
            [[NSNotificationCenter defaultCenter] removeObserver:gLaunchObserver];
            gLaunchObserver = nil;
        }
        if (gWakeObserver != nil) {
            [[[NSWorkspace sharedWorkspace] notificationCenter] removeObserver:gWakeObserver];
            gWakeObserver = nil;
        }
        if (gAppNapActivity != nil) {
            [[NSProcessInfo processInfo] endActivity:gAppNapActivity];
            gAppNapActivity = nil;
        }
        if (gController != nil) {
            [gController stop];
            gController = nil;
        }
    });
}
