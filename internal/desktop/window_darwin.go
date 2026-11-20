//go:build darwin

package desktop

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

@interface AppDelegate : NSObject <NSApplicationDelegate, WKUIDelegate>
@property (strong) NSWindow *window;
@property (strong) WKWebView *webView;
@property (copy) NSString *targetURL;
@end

@implementation AppDelegate

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    // Build standard macOS menu bar with Edit shortcuts (Cmd+C, Cmd+V, Cmd+A, Cmd+Q)
    NSMenu *menubar = [[NSMenu alloc] init];
    NSMenuItem *appMenuItem = [[NSMenuItem alloc] init];
    [menubar addItem:appMenuItem];
    NSMenu *appMenu = [[NSMenu alloc] init];
    [appMenu addItemWithTitle:@"Quit FreeGPU" action:@selector(terminate:) keyEquivalent:@"q"];
    [appMenuItem setSubmenu:appMenu];

    NSMenuItem *editMenuItem = [[NSMenuItem alloc] init];
    [menubar addItem:editMenuItem];
    NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"Edit"];
    [editMenu addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
    [editMenu addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
    [editMenu addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
    [editMenu addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];
    [editMenuItem setSubmenu:editMenu];
    [NSApp setMainMenu:menubar];

    NSRect frame = NSMakeRect(0, 0, 1240, 780);
    NSUInteger style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
    self.window = [[NSWindow alloc] initWithContentRect:frame styleMask:style backing:NSBackingStoreBuffered defer:NO];
    [self.window setTitle:@"FreeGPU — Colab GPU Terminal"];
    [self.window center];
    [self.window setBackgroundColor:[NSColor colorWithRed:0.04 green:0.04 blue:0.04 alpha:1.0]];

    WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
    self.webView = [[WKWebView alloc] initWithFrame:[[self.window contentView] bounds] configuration:config];
    [self.webView setAutoresizingMask:(NSViewWidthSizable | NSViewHeightSizable)];
    [self.webView setUIDelegate:self];

    NSURL *url = [NSURL URLWithString:self.targetURL];
    NSURLRequest *req = [NSURLRequest requestWithURL:url];
    [self.webView loadRequest:req];

    [[self.window contentView] addSubview:self.webView];
    [self.window makeKeyAndOrderFront:nil];
    [NSApp activateIgnoringOtherApps:YES];
}

- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)sender {
    return YES;
}

// Support JavaScript prompt() inside WKWebView (for Editing Compute Units)
- (void)webView:(WKWebView *)webView runJavaScriptTextInputPanelWithPrompt:(NSString *)prompt defaultText:(NSString *)defaultText initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(NSString *result))completionHandler {
    NSAlert *alert = [[NSAlert alloc] init];
    [alert setMessageText:prompt];
    [alert addButtonWithTitle:@"OK"];
    [alert addButtonWithTitle:@"Cancel"];
    NSTextField *input = [[NSTextField alloc] initWithFrame:NSMakeRect(0, 0, 260, 24)];
    [input setStringValue:(defaultText ? defaultText : @"")];
    [alert setAccessoryView:input];
    NSModalResponse response = [alert runModal];
    if (response == NSAlertFirstButtonReturn) {
        [input validateEditing];
        completionHandler([input stringValue]);
    } else {
        completionHandler(nil);
    }
}

@end

static void LaunchNativeWindow(const char *cUrl) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        AppDelegate *del = [[AppDelegate alloc] init];
        del.targetURL = [NSString stringWithUTF8String:cUrl];
        [NSApp setDelegate:del];
        [NSApp run];
    }
}
*/
import "C"
import "unsafe"

// OpenWindow blocks on the main thread and opens a native macOS WKWebView desktop application window.
func OpenWindow(url string) {
	cStr := C.CString(url)
	defer C.free(unsafe.Pointer(cStr))
	C.LaunchNativeWindow(cStr)
}
