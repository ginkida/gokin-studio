#import <AppKit/AppKit.h>
#include <stdbool.h>
#include <stdlib.h>
#include <string.h>

static NSPanel *gokinQuickEntryPanel = nil;
static NSWindow *gokinQuickEntryHostWindow = nil;
static NSView *gokinQuickEntryHostedView = nil;
static NSView *gokinQuickEntryHostPlaceholder = nil;
static BOOL gokinQuickEntryHostWasVisible = NO;
static BOOL gokinQuickEntryHostWasMiniaturized = NO;
static BOOL gokinQuickEntryHostWasKey = NO;
static BOOL gokinQuickEntryAppWasHidden = NO;

static void gokinQuickEntrySetError(char **errorOut, NSString *message) {
    if (errorOut == NULL) return;
    const char *utf8 = [message UTF8String];
    *errorOut = utf8 == NULL ? NULL : strdup(utf8);
}

static BOOL gokinViewContainsWebView(NSView *view) {
    if (view == nil) return NO;
    Class webViewClass = NSClassFromString(@"WKWebView");
    if (webViewClass != Nil && [view isKindOfClass:webViewClass]) return YES;
    for (NSView *child in [view subviews]) {
        if (gokinViewContainsWebView(child)) return YES;
    }
    return NO;
}

static NSWindow *gokinFindWailsWindow(void) {
    if (gokinQuickEntryHostWindow != nil) return gokinQuickEntryHostWindow;
    for (NSWindow *window in [NSApp windows]) {
        if (window == gokinQuickEntryPanel || [window isKindOfClass:[NSPanel class]]) continue;
        if (gokinViewContainsWebView([window contentView])) return window;
    }
    return nil;
}

static NSScreen *gokinQuickEntryTargetScreen(void) {
    NSPoint mouse = [NSEvent mouseLocation];
    for (NSScreen *screen in [NSScreen screens]) {
        if (NSPointInRect(mouse, [screen frame])) return screen;
    }
    return [NSScreen mainScreen];
}

static NSRect gokinQuickEntryFrame(void) {
    NSScreen *screen = gokinQuickEntryTargetScreen();
    NSRect visible = screen == nil ? NSMakeRect(0, 0, 1440, 900) : [screen visibleFrame];
    CGFloat width = MIN(720.0, MAX(520.0, visible.size.width - 48.0));
    CGFloat height = MIN(560.0, MAX(440.0, visible.size.height - 64.0));
    CGFloat x = NSMidX(visible) - width / 2.0;
    CGFloat y = NSMidY(visible) - height / 2.0 + MIN(80.0, visible.size.height * 0.08);
    return NSMakeRect(round(x), round(y), round(width), round(height));
}

static void gokinConfigureQuickEntryPanel(NSPanel *panel) {
    [panel setTitle:@"Quick Entry — Gokin Studio"];
    [panel setTitleVisibility:NSWindowTitleHidden];
    [panel setTitlebarAppearsTransparent:YES];
    [panel setMovableByWindowBackground:YES];
    [panel setFloatingPanel:YES];
    [panel setLevel:NSFloatingWindowLevel];
    [panel setHidesOnDeactivate:NO];
    [panel setReleasedWhenClosed:NO];
    [panel setCollectionBehavior:NSWindowCollectionBehaviorMoveToActiveSpace |
                                 NSWindowCollectionBehaviorFullScreenAuxiliary];
    [panel setBackgroundColor:[NSColor colorWithCalibratedRed:0.094 green:0.094 blue:0.106 alpha:1.0]];
    [panel setOpaque:YES];
    [panel setHasShadow:YES];
    [panel setMinSize:NSMakeSize(520.0, 440.0)];
}

static BOOL gokinQuickEntryShowOnMain(char **errorOut) {
    if (gokinQuickEntryPanel != nil && [gokinQuickEntryPanel isVisible]) {
        [NSApp unhide:nil];
        [NSApp activateIgnoringOtherApps:YES];
        [gokinQuickEntryPanel makeKeyAndOrderFront:nil];
        return YES;
    }

    NSWindow *host = gokinFindWailsWindow();
    if (host == nil || [host contentView] == nil) {
        gokinQuickEntrySetError(errorOut, @"the Wails window is not ready");
        return NO;
    }

    gokinQuickEntryHostWindow = [host retain];
    gokinQuickEntryHostedView = [[host contentView] retain];
    gokinQuickEntryHostWasVisible = [host isVisible];
    gokinQuickEntryHostWasMiniaturized = [host isMiniaturized];
    gokinQuickEntryHostWasKey = [host isKeyWindow];
    gokinQuickEntryAppWasHidden = [NSApp isHidden];

    if (gokinQuickEntryHostWasMiniaturized) [host deminiaturize:nil];
    gokinQuickEntryHostPlaceholder = [[NSView alloc] initWithFrame:[[host contentView] frame]];
    [gokinQuickEntryHostPlaceholder setAutoresizingMask:NSViewWidthSizable | NSViewHeightSizable];
    [host setContentView:gokinQuickEntryHostPlaceholder];
    [host orderOut:nil];

    NSWindowStyleMask style = NSWindowStyleMaskTitled |
                              NSWindowStyleMaskFullSizeContentView |
                              NSWindowStyleMaskResizable;
    gokinQuickEntryPanel = [[NSPanel alloc] initWithContentRect:gokinQuickEntryFrame()
                                                      styleMask:style
                                                        backing:NSBackingStoreBuffered
                                                          defer:NO];
    gokinConfigureQuickEntryPanel(gokinQuickEntryPanel);
    [gokinQuickEntryHostedView setAutoresizingMask:NSViewWidthSizable | NSViewHeightSizable];
    [gokinQuickEntryPanel setContentView:gokinQuickEntryHostedView];
    [NSApp unhide:nil];
    [NSApp activateIgnoringOtherApps:YES];
    [gokinQuickEntryPanel makeKeyAndOrderFront:nil];
    [gokinQuickEntryPanel makeFirstResponder:gokinQuickEntryHostedView];
    return YES;
}

static BOOL gokinQuickEntryHideOnMain(BOOL activateStudio, char **errorOut) {
    if (gokinQuickEntryPanel == nil) return YES;
    if (gokinQuickEntryHostWindow == nil || gokinQuickEntryHostedView == nil) {
        gokinQuickEntrySetError(errorOut, @"the original Wails window was lost");
        return NO;
    }

    [gokinQuickEntryPanel orderOut:nil];
    NSView *panelPlaceholder = [[[NSView alloc] initWithFrame:[[gokinQuickEntryPanel contentView] frame]] autorelease];
    [gokinQuickEntryPanel setContentView:panelPlaceholder];
    [gokinQuickEntryHostWindow setContentView:gokinQuickEntryHostedView];

    if (activateStudio) {
        if (gokinQuickEntryHostWasMiniaturized) [gokinQuickEntryHostWindow deminiaturize:nil];
        [NSApp unhide:nil];
        [NSApp activateIgnoringOtherApps:YES];
        [gokinQuickEntryHostWindow makeKeyAndOrderFront:nil];
    } else if (gokinQuickEntryAppWasHidden) {
        [gokinQuickEntryHostWindow orderFront:nil];
        [NSApp hide:nil];
    } else if (gokinQuickEntryHostWasMiniaturized) {
        [gokinQuickEntryHostWindow miniaturize:nil];
    } else if (gokinQuickEntryHostWasVisible) {
        [gokinQuickEntryHostWindow orderFront:nil];
        if (gokinQuickEntryHostWasKey) [gokinQuickEntryHostWindow makeKeyWindow];
    } else {
        [gokinQuickEntryHostWindow orderOut:nil];
    }

    [gokinQuickEntryPanel close];
    [gokinQuickEntryPanel release];
    gokinQuickEntryPanel = nil;
    [gokinQuickEntryHostedView release];
    gokinQuickEntryHostedView = nil;
    [gokinQuickEntryHostPlaceholder release];
    gokinQuickEntryHostPlaceholder = nil;
    [gokinQuickEntryHostWindow release];
    gokinQuickEntryHostWindow = nil;
    gokinQuickEntryAppWasHidden = NO;
    return YES;
}

bool gokinQuickEntryPanelShow(char **errorOut) {
    if (errorOut != NULL) *errorOut = NULL;
    __block BOOL result = NO;
    void (^operation)(void) = ^{ result = gokinQuickEntryShowOnMain(errorOut); };
    if ([NSThread isMainThread]) operation();
    else dispatch_sync(dispatch_get_main_queue(), operation);
    return result;
}

bool gokinQuickEntryPanelHide(bool activateStudio, char **errorOut) {
    if (errorOut != NULL) *errorOut = NULL;
    __block BOOL result = NO;
    void (^operation)(void) = ^{ result = gokinQuickEntryHideOnMain(activateStudio ? YES : NO, errorOut); };
    if ([NSThread isMainThread]) operation();
    else dispatch_sync(dispatch_get_main_queue(), operation);
    return result;
}

void gokinQuickEntryPanelFree(char *value) {
    free(value);
}
