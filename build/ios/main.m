//go:build ios
// Bootstrap: delegate comes from Go archive (WailsAppDelegate); the scene
// delegate below satisfies the UIScene lifecycle requirement (TN3187) that
// is fatal when building with the iOS 27 SDK.
#import <UIKit/UIKit.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>
#import <objc/message.h>
#include <stdio.h>
#include <stdbool.h>

// WailsViewController insets the WebView to the safe area, so the home-indicator
// zone shows the raw window background. Wrap its layout to re-pin the WebView to
// the full window; notch/gesture-bar avoidance is handled by CSS safe-area rules.
static void nxSwizzleFullScreenWebViewLayout(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        Class cls = NSClassFromString(@"WailsViewController");
        SEL sel = @selector(viewDidLayoutSubviews);
        Method method = cls ? class_getInstanceMethod(cls, sel) : NULL;
        if (!method) return;
        IMP original = method_getImplementation(method);
        IMP replacement = imp_implementationWithBlock(^(UIViewController *vc) {
            ((void (*)(id, SEL))original)(vc, sel);
            CGFloat bottom = 0;
            UITabBar *tabBar = [vc valueForKey:@"tabBar"];
            if (tabBar && !tabBar.hidden) bottom = tabBar.bounds.size.height;
            UIView *webView = [vc valueForKey:@"webView"];
            if (webView) webView.frame = UIEdgeInsetsInsetRect(vc.view.bounds, UIEdgeInsetsMake(0, 0, bottom, 0));
        });
        method_setImplementation(method, replacement);
    });
}

#define WailsEventApplicationDidBecomeActive 1245
#define WailsEventApplicationDidEnterBackground 1246
#define WailsEventApplicationWillEnterForeground 1249
#define WailsEventApplicationWillResignActive 1250

extern bool hasListeners(unsigned int);
extern void processApplicationEvent(unsigned int, void* data);

@interface WailsSceneDelegate : UIResponder <UIWindowSceneDelegate>
@property (strong, nonatomic) UIWindow *window;
@end

@implementation WailsSceneDelegate

- (void)scene:(UIScene *)scene willConnectToSession:(UISceneSession *)session options:(UISceneConnectionOptions *)connectionOptions {
    if (![scene isKindOfClass:[UIWindowScene class]]) {
        return;
    }
    nxSwizzleFullScreenWebViewLayout();
    UIWindowScene *windowScene = (UIWindowScene *)scene;
    id appDelegate = [UIApplication sharedApplication].delegate;
    UIWindow *legacyWindow = nil;
    if ([appDelegate respondsToSelector:@selector(window)]) {
        legacyWindow = [appDelegate valueForKey:@"window"];
    }
    UIWindow *sceneWindow = [[UIWindow alloc] initWithWindowScene:windowScene];
    sceneWindow.backgroundColor = legacyWindow.backgroundColor ?: [UIColor whiteColor];
    sceneWindow.overrideUserInterfaceStyle = UIUserInterfaceStyleDark;
    UIViewController *rootVC = legacyWindow.rootViewController;
    if (rootVC == nil) {
        rootVC = [[UIViewController alloc] init];
        rootVC.view.backgroundColor = sceneWindow.backgroundColor;
    }
    sceneWindow.rootViewController = rootVC;
    [sceneWindow makeKeyAndVisible];
    if ([appDelegate respondsToSelector:@selector(setWindow:)]) {
        [appDelegate setValue:sceneWindow forKey:@"window"];
    }
    self.window = sceneWindow;
}

- (void)sceneDidBecomeActive:(UIScene *)scene {
    if (hasListeners(WailsEventApplicationDidBecomeActive)) {
        processApplicationEvent(WailsEventApplicationDidBecomeActive, NULL);
    }
}

- (void)sceneWillResignActive:(UIScene *)scene {
    if (hasListeners(WailsEventApplicationWillResignActive)) {
        processApplicationEvent(WailsEventApplicationWillResignActive, NULL);
    }
}

- (void)sceneDidEnterBackground:(UIScene *)scene {
    if (hasListeners(WailsEventApplicationDidEnterBackground)) {
        processApplicationEvent(WailsEventApplicationDidEnterBackground, NULL);
    }
}

- (void)sceneWillEnterForeground:(UIScene *)scene {
    if (hasListeners(WailsEventApplicationWillEnterForeground)) {
        processApplicationEvent(WailsEventApplicationWillEnterForeground, NULL);
    }
}

@end

int main(int argc, char * argv[]) {
    @autoreleasepool {
        // Disable buffering so stdout/stderr from Go log.Printf flush immediately
        setvbuf(stdout, NULL, _IONBF, 0);
        setvbuf(stderr, NULL, _IONBF, 0);

        // Call UIApplicationMain IMMEDIATELY and start NOTHING else here. Do not
        // start the Go runtime yet: starting it concurrently with UIApplicationMain
        // intermittently corrupts the FrontBoard launch handshake on a physical
        // device, so the app delegate's didFinishLaunchingWithOptions never fires
        // (blank cold launch / 0x8BADF00D). Instead, the WailsAppDelegate (provided
        // by the Go archive) starts the Go runtime itself from
        // didFinishLaunchingWithOptions — i.e. only AFTER UIKit has delivered the
        // launch — so the runtime never races the launch handshake.
        return UIApplicationMain(argc, argv, nil, @"WailsAppDelegate");
    }
}
