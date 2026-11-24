//go:build darwin

#ifndef MORE_ACCOUNT_STATUSBAR_TESTBRIDGE_H
#define MORE_ACCOUNT_STATUSBAR_TESTBRIDGE_H

#include <stdbool.h>

void MATestRunAppLoop(void);
void MATestStopAppLoop(void);
void MATestSetupCocoaFixture(void);
void MATestCleanupCocoaFixture(void);
bool MATestAppRespondsToReopen(void);
bool MATestTriggerWindowShouldClose(void);
bool MATestTriggerAppReopen(bool hasVisibleWindows);
bool MATestSendReopenAppleEvent(void);
void MATestHideWindowOrderOut(void);
void MATestHideApp(void);
void MATestSetSimulatedMiniaturized(bool miniaturized);
bool MATestIsWindowVisible(void);
bool MATestIsWindowMiniaturized(void);
bool MATestIsAppHidden(void);
int MATestOrigCloseCount(void);
void MATestResetOrigCloseCount(void);
int MATestFinishLaunchingCount(void);
int MATestDeminiaturizeCount(void);

#endif // MORE_ACCOUNT_STATUSBAR_TESTBRIDGE_H
