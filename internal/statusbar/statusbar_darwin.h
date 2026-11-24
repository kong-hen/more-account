//go:build darwin

#ifndef MORE_ACCOUNT_STATUSBAR_DARWIN_H
#define MORE_ACCOUNT_STATUSBAR_DARWIN_H

#include <stdbool.h>
#include <stddef.h>

// 由 Objective-C 原生层导出的 C 控制接口（均内部保障主线程安全）。
void MAStatusBarStart(const void *iconBytes, int iconLen, bool closeToBackground);
void MAStatusBarSetCloseToBackground(bool enabled);
void MAStatusBarSetPreventAppNap(bool enabled);
void MAStatusBarShowMainWindow(void);
void MAStatusBarUpdateJSON(const char *snapshotJSON);
void MAStatusBarStop(void);

// 由 Go 层 //export 提供的回调接口。
extern char *maStatusBarFetchSnapshotJSON(void);
extern void maStatusBarOnShowWindow(void);
extern void maStatusBarOnSwitchAccount(char *accountID);
extern void maStatusBarOnRefreshCurrent(void);
extern void maStatusBarOnRefreshAll(void);
extern void maStatusBarOnTogglePrivacy(void);
extern void maStatusBarOnQuit(void);
extern void maStatusBarOnSystemWake(void);

#endif // MORE_ACCOUNT_STATUSBAR_DARWIN_H
