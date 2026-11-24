//go:build darwin

#import <Cocoa/Cocoa.h>
#import <CoreFoundation/CoreFoundation.h>
#import "bridge_darwin.h"

@interface MATestAppDelegate : NSObject <NSApplicationDelegate>
@property (nonatomic, assign) int finishLaunchingCount;
@end

@implementation MATestAppDelegate
- (void)applicationDidFinishLaunching:(NSNotification *)notification {
	(void)notification;
	self.finishLaunchingCount += 1;
}
@end

@interface MATestWindow : NSWindow
@property (nonatomic, assign) BOOL simulatedMiniaturized;
@property (nonatomic, assign) int deminiaturizeCallCount;
@end

@implementation MATestWindow
- (BOOL)isMiniaturized {
	return self.simulatedMiniaturized || [super isMiniaturized];
}
- (void)deminiaturize:(id)sender {
	self.deminiaturizeCallCount += 1;
	self.simulatedMiniaturized = NO;
	if ([super isMiniaturized]) {
		[super deminiaturize:sender];
	}
}
@end

@interface MATestWindowDelegate : NSObject <NSWindowDelegate>
@property (nonatomic, assign) int origCloseCount;
@property (nonatomic, assign) BOOL origCloseResult;
@end

@implementation MATestWindowDelegate
- (BOOL)windowShouldClose:(id)sender {
	(void)sender;
	self.origCloseCount += 1;
	return self.origCloseResult;
}
@end

static MATestWindow *gTestWin = nil;
static MATestWindowDelegate *gTestWinDel = nil;
static MATestAppDelegate *gTestAppDel = nil;

void MATestRunAppLoop(void) {
	@autoreleasepool {
		NSApplication *app = [NSApplication sharedApplication];
		[app setActivationPolicy:NSApplicationActivationPolicyRegular];
		if (gTestAppDel == nil) {
			gTestAppDel = [MATestAppDelegate new];
			[app setDelegate:gTestAppDel];
		}
		[app run];
	}
}

void MATestStopAppLoop(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSApplication *app = [NSApplication sharedApplication];
		[app stop:nil];
		NSEvent *ev = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
										 location:NSZeroPoint
									modifierFlags:0
										timestamp:0
									 windowNumber:0
										  context:nil
										  subtype:0
											data1:0
											data2:0];
		[app postEvent:ev atStart:YES];
	});
}

void MATestSetupCocoaFixture(void) {
	void (^setup)(void) = ^{
		NSApplication *app = [NSApplication sharedApplication];
		[app setActivationPolicy:NSApplicationActivationPolicyRegular];
		if (gTestAppDel == nil) {
			gTestAppDel = [MATestAppDelegate new];
			[app setDelegate:gTestAppDel];
		}
		if (gTestWin == nil) {
			gTestWin = [[MATestWindow alloc] initWithContentRect:NSMakeRect(-10000, -10000, 320, 240)
													   styleMask:(NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable)
														 backing:NSBackingStoreBuffered
														   defer:NO];
			// 使用全透明且离屏窗口，避免单元测试期间闪烁或抢占屏幕焦点
			[gTestWin setAlphaValue:0.0];
			[gTestWin setReleasedWhenClosed:NO];
			gTestWinDel = [MATestWindowDelegate new];
			gTestWinDel.origCloseResult = NO;
			[gTestWin setDelegate:gTestWinDel];
		}
		if ([app isHidden]) {
			[app unhideWithoutActivation];
		}
		if ([gTestWin isMiniaturized]) {
			[gTestWin deminiaturize:nil];
		}
		[gTestWin orderFront:nil];
	};
	if ([NSThread isMainThread]) {
		setup();
	} else {
		dispatch_sync(dispatch_get_main_queue(), setup);
	}
}

void MATestCleanupCocoaFixture(void) {
	void (^cleanup)(void) = ^{
		if (gTestWin != nil) {
			[gTestWin orderOut:nil];
		}
	};
	if ([NSThread isMainThread]) {
		cleanup();
	} else {
		dispatch_sync(dispatch_get_main_queue(), cleanup);
	}
}

bool MATestAppRespondsToReopen(void) {
	__block BOOL responds = NO;
	dispatch_sync(dispatch_get_main_queue(), ^{
		id del = [[NSApplication sharedApplication] delegate];
		responds = (del != nil && [del respondsToSelector:@selector(applicationShouldHandleReopen:hasVisibleWindows:)]);
	});
	return responds;
}

bool MATestTriggerWindowShouldClose(void) {
	__block BOOL allowed = YES;
	dispatch_sync(dispatch_get_main_queue(), ^{
		if (gTestWin != nil && gTestWinDel != nil) {
			allowed = [gTestWinDel windowShouldClose:gTestWin];
		}
	});
	return allowed;
}

bool MATestTriggerAppReopen(bool hasVisibleWindows) {
	__block BOOL res = NO;
	dispatch_sync(dispatch_get_main_queue(), ^{
		NSApplication *app = [NSApplication sharedApplication];
		id<NSApplicationDelegate> del = [app delegate];
		SEL sel = @selector(applicationShouldHandleReopen:hasVisibleWindows:);
		if (del != nil && [del respondsToSelector:sel]) {
			res = [del applicationShouldHandleReopen:app hasVisibleWindows:(hasVisibleWindows ? YES : NO)];
		}
	});
	return res;
}

bool MATestSendReopenAppleEvent(void) {
	__block OSStatus status = -1;
	dispatch_sync(dispatch_get_main_queue(), ^{
		ProcessSerialNumber psn = { 0, kCurrentProcess };
		NSAppleEventDescriptor *target = [NSAppleEventDescriptor descriptorWithDescriptorType:typeProcessSerialNumber
																						  bytes:&psn
																						 length:sizeof(psn)];
		NSAppleEventDescriptor *ae = [NSAppleEventDescriptor appleEventWithEventClass:kCoreEventClass
																			  eventID:kAEReopenApplication
																	 targetDescriptor:target
																			 returnID:kAutoGenerateReturnID
																		transactionID:kAnyTransactionID];
		AEDesc reply = { typeNull, NULL };
		AppleEvent aeCopy = { typeNull, NULL };
		if (AEDuplicateDesc(ae.aeDesc, &aeCopy) == noErr) {
			status = AESendMessage(&aeCopy, &reply, kAENoReply, kAEDefaultTimeout);
			AEDisposeDesc(&aeCopy);
			AEDisposeDesc(&reply);
		}
		if (status != noErr) {
			NSLog(@"MATestSendReopenAppleEvent failed: OSStatus=%d", (int)status);
		}
	});
	return status == noErr;
}

void MATestHideWindowOrderOut(void) {
	dispatch_sync(dispatch_get_main_queue(), ^{
		if (gTestWin != nil) {
			[gTestWin orderOut:nil];
		}
	});
}

void MATestHideApp(void) {
	dispatch_sync(dispatch_get_main_queue(), ^{
		[[NSApplication sharedApplication] hide:nil];
	});
}

void MATestSetSimulatedMiniaturized(bool miniaturized) {
	dispatch_sync(dispatch_get_main_queue(), ^{
		if (gTestWin != nil) {
			gTestWin.simulatedMiniaturized = (miniaturized ? YES : NO);
			if (miniaturized) {
				[gTestWin orderOut:nil];
			}
		}
	});
}

bool MATestIsWindowVisible(void) {
	__block BOOL visible = NO;
	dispatch_sync(dispatch_get_main_queue(), ^{
		visible = (gTestWin != nil && [gTestWin isVisible]);
	});
	return visible;
}

bool MATestIsWindowMiniaturized(void) {
	__block BOOL miniaturized = NO;
	dispatch_sync(dispatch_get_main_queue(), ^{
		miniaturized = (gTestWin != nil && [gTestWin isMiniaturized]);
	});
	return miniaturized;
}

bool MATestIsAppHidden(void) {
	__block BOOL hidden = NO;
	dispatch_sync(dispatch_get_main_queue(), ^{
		hidden = [[NSApplication sharedApplication] isHidden];
	});
	return hidden;
}

int MATestOrigCloseCount(void) {
	__block int count = 0;
	dispatch_sync(dispatch_get_main_queue(), ^{
		count = gTestWinDel != nil ? gTestWinDel.origCloseCount : 0;
	});
	return count;
}

void MATestResetOrigCloseCount(void) {
	dispatch_sync(dispatch_get_main_queue(), ^{
		if (gTestWinDel != nil) {
			gTestWinDel.origCloseCount = 0;
		}
	});
}

int MATestFinishLaunchingCount(void) {
	__block int count = 0;
	dispatch_sync(dispatch_get_main_queue(), ^{
		count = gTestAppDel != nil ? gTestAppDel.finishLaunchingCount : 0;
	});
	return count;
}

int MATestDeminiaturizeCount(void) {
	__block int count = 0;
	dispatch_sync(dispatch_get_main_queue(), ^{
		count = gTestWin != nil ? gTestWin.deminiaturizeCallCount : 0;
	});
	return count;
}
