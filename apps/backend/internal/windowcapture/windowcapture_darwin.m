#import "windowcapture_darwin.h"

#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>
#import <CoreGraphics/CoreGraphics.h>
#import <ImageIO/ImageIO.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <float.h>
#include <math.h>

@interface TLWindowCapture : NSObject
@property(nonatomic, retain) SCContentFilter *filter;
@property(nonatomic, assign) CGWindowID windowID;
@property(nonatomic, assign) pid_t processID;
@property(nonatomic, copy) NSString *title;
@property(nonatomic, assign) int maxWidth;
@property(nonatomic, assign) int maxHeight;
@end

@implementation TLWindowCapture
- (void)dealloc {
  [_filter release];
  [_title release];
  [super dealloc];
}
@end

static void TLSetError(char **target, NSString *message) {
  if (target == NULL) return;
  const char *utf8 = (message ?: @"macOS window capture failed").UTF8String;
  *target = strdup(utf8 ?: "macOS window capture failed");
}

static SCShareableContent *TLShareableContent(NSError **outError) {
  __block SCShareableContent *result = nil;
  __block NSError *failure = nil;
  dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
  [SCShareableContent getShareableContentExcludingDesktopWindows:YES onScreenWindowsOnly:YES completionHandler:^(SCShareableContent *content, NSError *error) {
    result = [content retain];
    failure = [error retain];
    dispatch_semaphore_signal(semaphore);
  }];
  long waitResult = dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, 8 * NSEC_PER_SEC));
  dispatch_release(semaphore);
  if (waitResult != 0) {
    if (outError) *outError = [NSError errorWithDomain:@"TermlinksWindowCapture" code:1 userInfo:@{NSLocalizedDescriptionKey: @"Timed out while asking macOS for the window list"}];
    [failure release];
    return nil;
  }
  if (failure != nil && outError) *outError = [failure autorelease];
  else [failure release];
  return [result autorelease];
}

static SCWindow *TLFindWindow(SCShareableContent *content, CGWindowID windowID) {
  for (SCWindow *window in content.windows) {
    if (window.windowID == windowID) return window;
  }
  return nil;
}

static CGRect TLWindowBounds(CGWindowID windowID) {
  CGRect result = CGRectNull;
  CFArrayRef list = CGWindowListCopyWindowInfo(kCGWindowListOptionIncludingWindow, windowID);
  if (list != NULL && CFArrayGetCount(list) > 0) {
    CFDictionaryRef info = CFArrayGetValueAtIndex(list, 0);
    CFDictionaryRef bounds = CFDictionaryGetValue(info, kCGWindowBounds);
    if (bounds != NULL) CGRectMakeWithDictionaryRepresentation(bounds, &result);
  }
  if (list != NULL) CFRelease(list);
  return result;
}

static void TLFocusWindow(TLWindowCapture *capture) {
  NSRunningApplication *application = [NSRunningApplication runningApplicationWithProcessIdentifier:capture.processID];
  [application activateWithOptions:NSApplicationActivateAllWindows];

  if (!AXIsProcessTrusted()) return;
  AXUIElementRef appElement = AXUIElementCreateApplication(capture.processID);
  CFTypeRef windowsValue = NULL;
  if (AXUIElementCopyAttributeValue(appElement, kAXWindowsAttribute, &windowsValue) == kAXErrorSuccess && windowsValue != NULL && CFGetTypeID(windowsValue) == CFArrayGetTypeID()) {
    CGRect wanted = TLWindowBounds(capture.windowID);
    AXUIElementRef best = NULL;
    double bestDistance = DBL_MAX;
    CFArrayRef windows = (CFArrayRef)windowsValue;
    for (CFIndex index = 0; index < CFArrayGetCount(windows); index++) {
      AXUIElementRef candidate = (AXUIElementRef)CFArrayGetValueAtIndex(windows, index);
      CFTypeRef positionValue = NULL;
      CFTypeRef sizeValue = NULL;
      CGPoint position = CGPointZero;
      CGSize size = CGSizeZero;
      if (AXUIElementCopyAttributeValue(candidate, kAXPositionAttribute, &positionValue) == kAXErrorSuccess &&
          AXUIElementCopyAttributeValue(candidate, kAXSizeAttribute, &sizeValue) == kAXErrorSuccess &&
          positionValue != NULL && sizeValue != NULL &&
          AXValueGetValue((AXValueRef)positionValue, kAXValueCGPointType, &position) &&
          AXValueGetValue((AXValueRef)sizeValue, kAXValueCGSizeType, &size)) {
        double distance = fabs(position.x - wanted.origin.x) + fabs(position.y - wanted.origin.y) +
                          fabs(size.width - wanted.size.width) + fabs(size.height - wanted.size.height);
        if (distance < bestDistance) {
          bestDistance = distance;
          best = candidate;
        }
      }
      if (positionValue != NULL) CFRelease(positionValue);
      if (sizeValue != NULL) CFRelease(sizeValue);
    }
    if (best != NULL) {
      AXUIElementPerformAction(best, kAXRaiseAction);
      AXUIElementSetAttributeValue(best, kAXMainAttribute, kCFBooleanTrue);
      AXUIElementSetAttributeValue(best, kAXFocusedAttribute, kCFBooleanTrue);
    }
    CFRelease(windowsValue);
  }
  CFRelease(appElement);
}

int tl_window_supported(void) {
  if (@available(macOS 14.0, *)) return 1;
  return 0;
}

int tl_screen_recording_allowed(void) { return CGPreflightScreenCaptureAccess() ? 1 : 0; }
int tl_accessibility_allowed(void) { return AXIsProcessTrusted() ? 1 : 0; }

void tl_request_permissions(void) {
  CGRequestScreenCaptureAccess();
  NSDictionary *options = @{(__bridge NSString *)kAXTrustedCheckOptionPrompt: @YES};
  AXIsProcessTrustedWithOptions((__bridge CFDictionaryRef)options);
}

int tl_window_sources(char **json, char **error) {
  @autoreleasepool {
    if (!tl_window_supported()) {
      TLSetError(error, @"Selected-window sharing requires macOS 14 or newer");
      return 0;
    }
    NSError *nativeError = nil;
    SCShareableContent *content = TLShareableContent(&nativeError);
    if (content == nil) {
      TLSetError(error, nativeError.localizedDescription ?: @"Allow Termlinks in System Settings → Privacy & Security → Screen & System Audio Recording");
      return 0;
    }
    NSMutableArray *items = [NSMutableArray array];
    for (SCWindow *window in content.windows) {
      NSString *application = window.owningApplication.applicationName ?: @"";
      NSString *title = window.title ?: @"";
      CGRect frame = window.frame;
      if (window.windowLayer != 0 || application.length == 0 || title.length == 0 || frame.size.width < 120 || frame.size.height < 80) continue;
      [items addObject:@{
        @"id": @(window.windowID),
        @"title": title,
        @"application": application,
        @"bundleId": window.owningApplication.bundleIdentifier ?: @"",
        @"width": @((NSInteger)frame.size.width),
        @"height": @((NSInteger)frame.size.height)
      }];
    }
    [items sortUsingComparator:^NSComparisonResult(NSDictionary *left, NSDictionary *right) {
      NSComparisonResult appResult = [left[@"application"] localizedCaseInsensitiveCompare:right[@"application"]];
      return appResult == NSOrderedSame ? [left[@"title"] localizedCaseInsensitiveCompare:right[@"title"]] : appResult;
    }];
    NSData *data = [NSJSONSerialization dataWithJSONObject:items options:0 error:&nativeError];
    if (data == nil) {
      TLSetError(error, nativeError.localizedDescription);
      return 0;
    }
    *json = malloc(data.length + 1);
    if (*json == NULL) {
      TLSetError(error, @"Could not allocate the macOS window list");
      return 0;
    }
    memcpy(*json, data.bytes, data.length);
    (*json)[data.length] = '\0';
    return 1;
  }
}

void *tl_window_open(uint32_t window_id, int max_width, int max_height, char **error) {
  @autoreleasepool {
    // SCContentFilter requires CoreGraphics' window-server client to be initialized
    // when ScreenCaptureKit is hosted by a command-line process.
    (void)CGMainDisplayID();
    NSError *nativeError = nil;
    SCShareableContent *content = TLShareableContent(&nativeError);
    if (content == nil) {
      TLSetError(error, nativeError.localizedDescription ?: @"Screen Recording permission is required");
      return NULL;
    }
    SCWindow *window = TLFindWindow(content, window_id);
    if (window == nil) {
      TLSetError(error, @"That window is no longer available; refresh the window list");
      return NULL;
    }
    TLWindowCapture *capture = [[TLWindowCapture alloc] init];
    capture.filter = [[[SCContentFilter alloc] initWithDesktopIndependentWindow:window] autorelease];
    capture.windowID = window.windowID;
    capture.processID = window.owningApplication.processID;
    capture.title = window.title ?: @"Selected window";
    capture.maxWidth = MAX(320, MIN(max_width, 2560));
    capture.maxHeight = MAX(240, MIN(max_height, 1800));
    return capture;
  }
}

int tl_window_frame(void *opaque, unsigned char **data, size_t *length, int *width, int *height, char **error) {
  @autoreleasepool {
    TLWindowCapture *capture = (TLWindowCapture *)opaque;
    if (capture == nil) {
      TLSetError(error, @"Window capture is closed");
      return 0;
    }
    CGRect contentRect = capture.filter.contentRect;
    double scale = capture.filter.pointPixelScale;
    if (scale <= 0) scale = 1;
    double sourceWidth = MAX(1, contentRect.size.width * scale);
    double sourceHeight = MAX(1, contentRect.size.height * scale);
    double fit = MIN(1.0, MIN((double)capture.maxWidth / sourceWidth, (double)capture.maxHeight / sourceHeight));
    size_t targetWidth = MAX(1, (size_t)llround(sourceWidth * fit));
    size_t targetHeight = MAX(1, (size_t)llround(sourceHeight * fit));

    SCStreamConfiguration *configuration = [[[SCStreamConfiguration alloc] init] autorelease];
    configuration.width = targetWidth;
    configuration.height = targetHeight;
    configuration.showsCursor = YES;
    configuration.ignoreShadowsSingleWindow = YES;
    configuration.shouldBeOpaque = YES;
    configuration.captureResolution = SCCaptureResolutionBest;

    __block CGImageRef image = NULL;
    __block NSError *nativeError = nil;
    dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
    [SCScreenshotManager captureImageWithFilter:capture.filter configuration:configuration completionHandler:^(CGImageRef result, NSError *failure) {
      if (result != NULL) image = CGImageRetain(result);
      nativeError = [failure retain];
      dispatch_semaphore_signal(semaphore);
    }];
    long waitResult = dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
    dispatch_release(semaphore);
    if (waitResult != 0 || image == NULL) {
      TLSetError(error, waitResult != 0 ? @"Timed out while capturing the selected window" : (nativeError.localizedDescription ?: @"The selected window could not be captured"));
      [nativeError release];
      if (image != NULL) CGImageRelease(image);
      return 0;
    }

    CFMutableDataRef encoded = CFDataCreateMutable(kCFAllocatorDefault, 0);
    CGImageDestinationRef destination = CGImageDestinationCreateWithData(encoded, CFSTR("public.jpeg"), 1, NULL);
    NSDictionary *properties = @{(__bridge NSString *)kCGImageDestinationLossyCompressionQuality: @0.62};
    CGImageDestinationAddImage(destination, image, (__bridge CFDictionaryRef)properties);
    bool finalized = CGImageDestinationFinalize(destination);
    CGImageRelease(image);
    CFRelease(destination);
    [nativeError release];
    if (!finalized || CFDataGetLength(encoded) == 0) {
      CFRelease(encoded);
      TLSetError(error, @"Could not encode the selected-window frame");
      return 0;
    }
    CFIndex encodedLength = CFDataGetLength(encoded);
    *data = malloc((size_t)encodedLength);
    if (*data == NULL) {
      CFRelease(encoded);
      TLSetError(error, @"Could not allocate the selected-window frame");
      return 0;
    }
    memcpy(*data, CFDataGetBytePtr(encoded), (size_t)encodedLength);
    *length = (size_t)encodedLength;
    // The image dimensions match the requested configuration.
    *width = (int)targetWidth;
    *height = (int)targetHeight;
    CFRelease(encoded);
    return 1;
  }
}

int tl_window_pointer(void *opaque, const char *action_value, double x, double y, int button, double delta_x, double delta_y) {
  @autoreleasepool {
    TLWindowCapture *capture = (TLWindowCapture *)opaque;
    if (capture == nil || !AXIsProcessTrusted()) return 0;
    NSString *action = [NSString stringWithUTF8String:action_value ?: ""];
    CGRect bounds = TLWindowBounds(capture.windowID);
    if (CGRectIsNull(bounds) || bounds.size.width <= 0 || bounds.size.height <= 0) return 0;
    CGPoint point = CGPointMake(bounds.origin.x + MIN(1, MAX(0, x)) * bounds.size.width,
                                bounds.origin.y + MIN(1, MAX(0, y)) * bounds.size.height);
    CGMouseButton mouseButton = button == 2 ? kCGMouseButtonRight : (button == 1 ? kCGMouseButtonCenter : kCGMouseButtonLeft);
    CGEventType eventType = kCGEventMouseMoved;
    if ([action isEqualToString:@"down"]) eventType = mouseButton == kCGMouseButtonRight ? kCGEventRightMouseDown : (mouseButton == kCGMouseButtonCenter ? kCGEventOtherMouseDown : kCGEventLeftMouseDown);
    else if ([action isEqualToString:@"up"]) eventType = mouseButton == kCGMouseButtonRight ? kCGEventRightMouseUp : (mouseButton == kCGMouseButtonCenter ? kCGEventOtherMouseUp : kCGEventLeftMouseUp);
    else if ([action isEqualToString:@"drag"]) eventType = mouseButton == kCGMouseButtonRight ? kCGEventRightMouseDragged : (mouseButton == kCGMouseButtonCenter ? kCGEventOtherMouseDragged : kCGEventLeftMouseDragged);
    else if ([action isEqualToString:@"scroll"]) {
      CGEventRef scroll = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitPixel, 2, (int32_t)-delta_y, (int32_t)-delta_x);
      if (scroll == NULL) return 0;
      CGEventPost(kCGHIDEventTap, scroll);
      CFRelease(scroll);
      return 1;
    }
    if ([action isEqualToString:@"down"]) TLFocusWindow(capture);
    CGEventRef event = CGEventCreateMouseEvent(NULL, eventType, point, mouseButton);
    if (event == NULL) return 0;
    CGEventPost(kCGHIDEventTap, event);
    CFRelease(event);
    return 1;
  }
}

static CGKeyCode TLKeyCode(NSString *code) {
  static NSDictionary *mapping = nil;
  if (mapping == nil) mapping = [@{
    @"KeyA": @0, @"KeyS": @1, @"KeyD": @2, @"KeyF": @3, @"KeyH": @4, @"KeyG": @5, @"KeyZ": @6, @"KeyX": @7, @"KeyC": @8, @"KeyV": @9,
    @"KeyB": @11, @"KeyQ": @12, @"KeyW": @13, @"KeyE": @14, @"KeyR": @15, @"KeyY": @16, @"KeyT": @17,
    @"Digit1": @18, @"Digit2": @19, @"Digit3": @20, @"Digit4": @21, @"Digit6": @22, @"Digit5": @23, @"Equal": @24, @"Digit9": @25, @"Digit7": @26, @"Minus": @27, @"Digit8": @28, @"Digit0": @29,
    @"BracketRight": @30, @"KeyO": @31, @"KeyU": @32, @"BracketLeft": @33, @"KeyI": @34, @"KeyP": @35, @"Enter": @36, @"KeyL": @37, @"KeyJ": @38, @"Quote": @39, @"KeyK": @40, @"Semicolon": @41, @"Backslash": @42, @"Comma": @43, @"Slash": @44, @"KeyN": @45, @"KeyM": @46, @"Period": @47,
    @"Tab": @48, @"Space": @49, @"Backquote": @50, @"Backspace": @51, @"Escape": @53,
    @"Home": @115, @"PageUp": @116, @"Delete": @117, @"End": @119, @"PageDown": @121, @"ArrowLeft": @123, @"ArrowRight": @124, @"ArrowDown": @125, @"ArrowUp": @126
  } retain];
  NSNumber *value = mapping[code];
  return value == nil ? UINT16_MAX : (CGKeyCode)value.unsignedShortValue;
}

int tl_window_key(void *opaque, const char *code_value, int down, int shift, int control, int option, int command) {
  @autoreleasepool {
    TLWindowCapture *capture = (TLWindowCapture *)opaque;
    if (capture == nil || !AXIsProcessTrusted()) return 0;
    NSString *code = [NSString stringWithUTF8String:code_value ?: ""];
    CGKeyCode keyCode = TLKeyCode(code);
    if (keyCode == UINT16_MAX) return 0;
    TLFocusWindow(capture);
    CGEventRef event = CGEventCreateKeyboardEvent(NULL, keyCode, down != 0);
    if (event == NULL) return 0;
    CGEventFlags flags = 0;
    if (shift) flags |= kCGEventFlagMaskShift;
    if (control) flags |= kCGEventFlagMaskControl;
    if (option) flags |= kCGEventFlagMaskAlternate;
    if (command) flags |= kCGEventFlagMaskCommand;
    CGEventSetFlags(event, flags);
    CGEventPost(kCGHIDEventTap, event);
    CFRelease(event);
    return 1;
  }
}

int tl_window_text(void *opaque, const char *text_value) {
  @autoreleasepool {
    TLWindowCapture *capture = (TLWindowCapture *)opaque;
    if (capture == nil || !AXIsProcessTrusted() || text_value == NULL) return 0;
    NSString *text = [NSString stringWithUTF8String:text_value];
    if (text == nil || text.length == 0) return 0;
    TLFocusWindow(capture);
    NSUInteger offset = 0;
    while (offset < text.length) {
      NSUInteger count = MIN((NSUInteger)20, text.length - offset);
      unichar buffer[20];
      [text getCharacters:buffer range:NSMakeRange(offset, count)];
      CGEventRef down = CGEventCreateKeyboardEvent(NULL, 0, true);
      CGEventRef up = CGEventCreateKeyboardEvent(NULL, 0, false);
      if (down == NULL || up == NULL) {
        if (down != NULL) CFRelease(down);
        if (up != NULL) CFRelease(up);
        return 0;
      }
      CGEventKeyboardSetUnicodeString(down, count, buffer);
      CGEventKeyboardSetUnicodeString(up, count, buffer);
      CGEventPost(kCGHIDEventTap, down);
      CGEventPost(kCGHIDEventTap, up);
      CFRelease(down);
      CFRelease(up);
      offset += count;
    }
    return 1;
  }
}

int tl_window_clipboard(void *opaque, const char *text_value) {
  @autoreleasepool {
    TLWindowCapture *capture = (TLWindowCapture *)opaque;
    if (capture == nil || text_value == NULL) return 0;
    NSString *text = [NSString stringWithUTF8String:text_value];
    if (text == nil) return 0;
    NSPasteboard *pasteboard = [NSPasteboard generalPasteboard];
    [pasteboard clearContents];
    return [pasteboard setString:text forType:NSPasteboardTypeString] ? 1 : 0;
  }
}

void tl_window_close(void *opaque) {
  @autoreleasepool {
    [(TLWindowCapture *)opaque release];
  }
}
