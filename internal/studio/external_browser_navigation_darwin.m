#import <Foundation/Foundation.h>
#import <WebKit/WebKit.h>
#import <dispatch/dispatch.h>
#import <objc/runtime.h>
#include <stdbool.h>
#include <strings.h>

// WailsContext is owned by Wails and already acts as WKNavigationDelegate.
// Wails v2.12 does not implement the policy callback. We install it through
// the Objective-C runtime instead of a category so package unit tests (which
// do not link the Wails desktop frontend) retain no hard class-symbol reference.

static bool gokinEqual(const char *left, const char *right) {
    if (left == NULL || right == NULL) return false;
    return strcasecmp(left, right) == 0;
}

static bool gokinLoopbackHost(const char *host) {
    if (host == NULL || host[0] == '\0') return false;
    if (gokinEqual(host, "localhost") || gokinEqual(host, "127.0.0.1") || gokinEqual(host, "::1")) return true;
    size_t length = strlen(host);
    static const char suffix[] = ".localhost";
    size_t suffixLength = sizeof(suffix) - 1;
    return length > suffixLength && strcasecmp(host + length - suffixLength, suffix) == 0;
}

bool gokinExternalNavigationPolicyAllows(
    const char *sourceScheme,
    const char *sourceHost,
    long sourcePort,
    bool sourceIsMainFrame,
    const char *targetScheme,
    const char *targetHost,
    long targetPort,
    bool targetIsMainFrame,
    bool hasTargetFrame
) {
    if (!hasTargetFrame || targetScheme == NULL || targetScheme[0] == '\0') return false;

    // Wails owns the main document. It may load only its custom origin or a
    // loopback development origin; external top-level navigation must use the
    // explicit BrowserOpenURL path instead.
    if (targetIsMainFrame) {
        if (gokinEqual(targetScheme, "wails")) return true;
        // WKWebView may report no source frame for its initial about:/dev URL.
        if ((sourceScheme == NULL || sourceScheme[0] == '\0') &&
            (gokinEqual(targetScheme, "about") ||
             ((gokinEqual(targetScheme, "http") || gokinEqual(targetScheme, "https")) && gokinLoopbackHost(targetHost)))) return true;
        if (!sourceIsMainFrame) return false;
        if (gokinEqual(targetScheme, "about")) return true;
        return (gokinEqual(targetScheme, "http") || gokinEqual(targetScheme, "https")) && gokinLoopbackHost(targetHost);
    }

    // srcdoc/about:blank are used by isolated artifacts and MCP Apps.
    if (gokinEqual(targetScheme, "about")) return true;
    // React may initially point an artifact/PDF iframe at an in-memory URL.
    // Untrusted child frames cannot manufacture a data/blob navigation for
    // themselves because their source frame is not the main Wails document.
    if (gokinEqual(targetScheme, "data") || gokinEqual(targetScheme, "blob")) return sourceIsMainFrame;
    if (!gokinEqual(targetScheme, "http") && !gokinEqual(targetScheme, "https")) return false;
    if (!gokinLoopbackHost(targetHost)) return false;

    // The main React document may initially point an iframe at a reviewed
    // loopback preview/proxy. Once code inside that frame initiates navigation,
    // it is confined to its exact local origin (scheme + host + port). This is
    // the native boundary that prevents an approved public page from changing
    // location to 127.0.0.1 or another loopback service.
    if (sourceIsMainFrame) return true;
    return gokinEqual(sourceScheme, targetScheme) &&
           gokinEqual(sourceHost, targetHost) &&
           sourcePort == targetPort;
}

static void gokinDecidePolicyForNavigationAction(
    id self,
    SEL command,
    WKWebView *webView,
    WKNavigationAction *navigationAction,
    void (^decisionHandler)(WKNavigationActionPolicy)
) {
    WKFrameInfo *source = navigationAction.sourceFrame;
    WKFrameInfo *target = navigationAction.targetFrame;
    NSURL *URL = navigationAction.request.URL;
    WKSecurityOrigin *origin = source.securityOrigin;
    NSString *sourceScheme = origin.protocol ?: @"";
    NSString *sourceHost = origin.host ?: @"";
    NSString *targetScheme = URL.scheme ?: @"";
    NSString *targetHost = URL.host ?: @"";
    long sourcePort = (long)origin.port;
    long targetPort = URL.port == nil ? ([targetScheme caseInsensitiveCompare:@"https"] == NSOrderedSame ? 443 : 80) : URL.port.longValue;
    bool allowed = gokinExternalNavigationPolicyAllows(
        sourceScheme.UTF8String,
        sourceHost.UTF8String,
        sourcePort,
        source != nil && source.mainFrame,
        targetScheme.UTF8String,
        targetHost.UTF8String,
        targetPort,
        target != nil && target.mainFrame,
        target != nil
    );
    decisionHandler(allowed ? WKNavigationActionPolicyAllow : WKNavigationActionPolicyCancel);
}

static bool gokinInstallExternalNavigationPolicy(Class context) {
    SEL selector = @selector(webView:decidePolicyForNavigationAction:decisionHandler:);
    if (context == Nil) return false;
    Method existing = class_getInstanceMethod(context, selector);
    if (existing != NULL) {
        return method_getImplementation(existing) == (IMP)gokinDecidePolicyForNavigationAction;
    }
    return class_addMethod(
        context,
        selector,
        (IMP)gokinDecidePolicyForNavigationAction,
        "v@:@@@?"
    );
}

bool gokinExternalNavigationPolicyAvailable(void) {
    return gokinInstallExternalNavigationPolicy(NSClassFromString(@"WailsContext"));
}

bool gokinExternalNavigationPolicyTestInstall(void) {
    static Class testContext = Nil;
    static dispatch_once_t onceToken;
    dispatch_once(&onceToken, ^{
        testContext = objc_allocateClassPair([NSObject class], "GokinNavigationPolicyTestContext", 0);
        if (testContext != Nil) objc_registerClassPair(testContext);
    });
    return gokinInstallExternalNavigationPolicy(testContext);
}
