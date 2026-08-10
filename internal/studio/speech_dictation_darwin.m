//go:build darwin && cgo

#import <Foundation/Foundation.h>
#import <Speech/Speech.h>
#import <AVFoundation/AVFoundation.h>
#import <AVFAudio/AVFAudio.h>
#include <stdbool.h>
#include <stdint.h>

extern void goNativeSpeechDarwinEvent(uint32_t token, int eventType, const char *text, const char *error, bool final);
extern void goNativeSpeechDarwinPermissionResult(uint32_t token, int speechStatus, int microphoneStatus);

enum {
    GokinSpeechEventAuthorizing = 0,
    GokinSpeechEventStarted = 1,
    GokinSpeechEventTranscript = 2,
    GokinSpeechEventStopping = 3,
    GokinSpeechEventEnded = 4,
    GokinSpeechEventError = 5,
};

API_AVAILABLE(macos(14.0)) @interface GokinSpeechController : NSObject {
    uint32_t _token;
    NSString *_localeIdentifier;
    SFSpeechRecognizer *_recognizer;
    SFSpeechAudioBufferRecognitionRequest *_request;
    SFSpeechRecognitionTask *_task;
    AVAudioEngine *_engine;
    BOOL _tapInstalled;
    BOOL _stopping;
    BOOL _finished;
}
- (instancetype)initWithToken:(uint32_t)token locale:(NSString *)locale;
- (void)start;
- (void)stop:(BOOL)cancel;
- (void)handleResult:(SFSpeechRecognitionResult *)result error:(NSError *)error;
@end

static NSMutableDictionary *gokinSpeechControllers(void) {
    static NSMutableDictionary *controllers = nil;
    static dispatch_once_t onceToken;
    dispatch_once(&onceToken, ^{
        controllers = [[NSMutableDictionary alloc] init];
    });
    return controllers;
}

static void gokinOnMainSync(dispatch_block_t block) {
    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_sync(dispatch_get_main_queue(), block);
    }
}

static void gokinEmitSpeech(uint32_t token, int type, NSString *text, NSString *error, BOOL final) {
    goNativeSpeechDarwinEvent(
        token,
        type,
        text != nil ? [text UTF8String] : NULL,
        error != nil ? [error UTF8String] : NULL,
        final
    );
}

static BOOL gokinSpeechRuntimeSupported(void) {
    if (@available(macOS 14.0, *)) {
        return NSClassFromString(@"SFSpeechRecognizer") != nil;
    }
    return NO;
}

static int gokinMicrophoneStatus(void) API_AVAILABLE(macos(14.0));
static int gokinMicrophoneStatus(void) {
    return (int)[AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
}

@implementation GokinSpeechController

- (instancetype)initWithToken:(uint32_t)token locale:(NSString *)locale {
    self = [super init];
    if (self != nil) {
        _token = token;
        _localeIdentifier = [locale copy];
    }
    return self;
}

- (void)dealloc {
    if (_tapInstalled) {
        @try {
            [[_engine inputNode] removeTapOnBus:0];
        } @catch (__unused NSException *exception) {
        }
    }
    [_engine stop];
    [_task cancel];
    [_task release];
    [_request release];
    [_recognizer release];
    [_engine release];
    [_localeIdentifier release];
    [super dealloc];
}

- (BOOL)isRegistered {
    return [gokinSpeechControllers() objectForKey:[NSNumber numberWithUnsignedInt:_token]] == self;
}

- (void)removeAudioTapAndStopEngine {
    if (_tapInstalled) {
        @try {
            [[_engine inputNode] removeTapOnBus:0];
        } @catch (__unused NSException *exception) {
        }
        _tapInstalled = NO;
    }
    if ([_engine isRunning]) {
        [_engine stop];
    }
}

- (void)removeFromRegistry {
    NSNumber *key = [NSNumber numberWithUnsignedInt:_token];
    [self retain];
    [gokinSpeechControllers() removeObjectForKey:key];
    [self autorelease];
}

- (void)finishEnded {
    if (_finished) {
        return;
    }
    _finished = YES;
    [self removeAudioTapAndStopEngine];
    gokinEmitSpeech(_token, GokinSpeechEventEnded, nil, nil, NO);
    [self removeFromRegistry];
}

- (void)finishWithError:(NSString *)message {
    if (_finished) {
        return;
    }
    _finished = YES;
    [self removeAudioTapAndStopEngine];
    [_task cancel];
    gokinEmitSpeech(_token, GokinSpeechEventError, nil, message ?: @"Native speech recognition failed.", NO);
    gokinEmitSpeech(_token, GokinSpeechEventEnded, nil, nil, NO);
    [self removeFromRegistry];
}

- (void)beginRecognition {
    if (_finished || ![self isRegistered]) {
        return;
    }
    NSLocale *locale = nil;
    if ([_localeIdentifier length] > 0) {
        locale = [[[NSLocale alloc] initWithLocaleIdentifier:_localeIdentifier] autorelease];
    }
    _recognizer = [[SFSpeechRecognizer alloc] initWithLocale:locale ?: [NSLocale currentLocale]];
    if (_recognizer == nil || ![_recognizer isAvailable]) {
        [self finishWithError:@"Apple Speech Recognition is currently unavailable for this language."];
        return;
    }

    _request = [[SFSpeechAudioBufferRecognitionRequest alloc] init];
    [_request setShouldReportPartialResults:YES];
    [_request setTaskHint:SFSpeechRecognitionTaskHintDictation];
    if ([_request respondsToSelector:@selector(setAddsPunctuation:)]) {
        [_request setAddsPunctuation:YES];
    }

    _engine = [[AVAudioEngine alloc] init];
    AVAudioInputNode *inputNode = [_engine inputNode];
    AVAudioFormat *format = [inputNode outputFormatForBus:0];
    if (format == nil || [format sampleRate] <= 0 || [format channelCount] == 0) {
        [self finishWithError:@"No working microphone input is available."];
        return;
    }

    uint32_t token = _token;
    SFSpeechAudioBufferRecognitionRequest *audioRequest = _request;
    @try {
        [inputNode installTapOnBus:0 bufferSize:1024 format:format block:^(AVAudioPCMBuffer *buffer, AVAudioTime *when) {
            [audioRequest appendAudioPCMBuffer:buffer];
        }];
        _tapInstalled = YES;
    } @catch (NSException *exception) {
        [self finishWithError:[NSString stringWithFormat:@"Could not access the microphone: %@", [exception reason]]];
        return;
    }

    _task = [[_recognizer recognitionTaskWithRequest:_request resultHandler:^(SFSpeechRecognitionResult *result, NSError *error) {
        dispatch_async(dispatch_get_main_queue(), ^{
            GokinSpeechController *controller = [gokinSpeechControllers() objectForKey:[NSNumber numberWithUnsignedInt:token]];
            [controller handleResult:result error:error];
        });
    }] retain];

    [_engine prepare];
    NSError *engineError = nil;
    if (![_engine startAndReturnError:&engineError]) {
        [self finishWithError:[NSString stringWithFormat:@"Could not start microphone capture: %@", [engineError localizedDescription]]];
        return;
    }
    gokinEmitSpeech(_token, GokinSpeechEventStarted, nil, nil, NO);
}

- (void)requestMicrophoneAndBegin {
    AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
    if (status == AVAuthorizationStatusAuthorized) {
        [self beginRecognition];
        return;
    }
    if (status == AVAuthorizationStatusDenied || status == AVAuthorizationStatusRestricted) {
        [self finishWithError:@"Microphone access is denied. Enable it in System Settings > Privacy & Security > Microphone."];
        return;
    }
    uint32_t token = _token;
    [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) {
        dispatch_async(dispatch_get_main_queue(), ^{
            GokinSpeechController *controller = [gokinSpeechControllers() objectForKey:[NSNumber numberWithUnsignedInt:token]];
            if (granted) {
                [controller beginRecognition];
            } else {
                [controller finishWithError:@"Microphone access was not granted."];
            }
        });
    }];
}

- (void)continueAfterSpeechAuthorization:(SFSpeechRecognizerAuthorizationStatus)status {
    if (_finished || ![self isRegistered]) {
        return;
    }
    if (status != SFSpeechRecognizerAuthorizationStatusAuthorized) {
        NSString *message = status == SFSpeechRecognizerAuthorizationStatusRestricted
            ? @"Speech Recognition is restricted on this Mac."
            : @"Speech Recognition access is denied. Enable it in System Settings > Privacy & Security > Speech Recognition.";
        [self finishWithError:message];
        return;
    }
    [self requestMicrophoneAndBegin];
}

- (void)start {
    gokinEmitSpeech(_token, GokinSpeechEventAuthorizing, nil, nil, NO);
    SFSpeechRecognizerAuthorizationStatus status = [SFSpeechRecognizer authorizationStatus];
    if (status == SFSpeechRecognizerAuthorizationStatusNotDetermined) {
        uint32_t token = _token;
        [SFSpeechRecognizer requestAuthorization:^(SFSpeechRecognizerAuthorizationStatus result) {
            dispatch_async(dispatch_get_main_queue(), ^{
                GokinSpeechController *controller = [gokinSpeechControllers() objectForKey:[NSNumber numberWithUnsignedInt:token]];
                [controller continueAfterSpeechAuthorization:result];
            });
        }];
        return;
    }
    [self continueAfterSpeechAuthorization:status];
}

- (void)handleResult:(SFSpeechRecognitionResult *)result error:(NSError *)error {
    if (_finished) {
        return;
    }
    if (result != nil) {
        NSString *text = [[[result bestTranscription] formattedString] copy];
        gokinEmitSpeech(_token, GokinSpeechEventTranscript, text, nil, [result isFinal]);
        [text release];
        if ([result isFinal]) {
            [self finishEnded];
            return;
        }
    }
    if (error != nil) {
        if (_stopping) {
            [self finishEnded];
        } else {
            [self finishWithError:[error localizedDescription]];
        }
    }
}

- (void)stop:(BOOL)cancel {
    if (_finished) {
        return;
    }
    if (cancel) {
        _finished = YES;
        [self removeAudioTapAndStopEngine];
        [_task cancel];
        gokinEmitSpeech(_token, GokinSpeechEventEnded, nil, nil, NO);
        [self removeFromRegistry];
        return;
    }
    if (_stopping) {
        return;
    }
    _stopping = YES;
    [self removeAudioTapAndStopEngine];
    [_request endAudio];
    [_task finish];
    gokinEmitSpeech(_token, GokinSpeechEventStopping, nil, nil, NO);
    uint32_t token = _token;
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(2 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
        GokinSpeechController *controller = [gokinSpeechControllers() objectForKey:[NSNumber numberWithUnsignedInt:token]];
        [controller finishEnded];
    });
}

@end

bool gokinSpeechSupported(void) {
    return gokinSpeechRuntimeSupported();
}

int gokinSpeechAuthorizationStatus(void) {
    if (@available(macOS 14.0, *)) {
        if (gokinSpeechRuntimeSupported()) {
            return (int)[SFSpeechRecognizer authorizationStatus];
        }
    }
    return -1;
}

int gokinSpeechMicrophoneAuthorizationStatus(void) {
    if (@available(macOS 14.0, *)) {
        if (gokinSpeechRuntimeSupported()) {
            return gokinMicrophoneStatus();
        }
    }
    return -1;
}

bool gokinSpeechRecognizerAvailable(const char *localeValue) {
    if (@available(macOS 14.0, *)) {
        if (!gokinSpeechRuntimeSupported()) {
            return false;
        }
        __block BOOL available = NO;
        NSString *localeString = localeValue != NULL ? [NSString stringWithUTF8String:localeValue] : @"";
        gokinOnMainSync(^{
            NSLocale *locale = [localeString length] > 0
                ? [[[NSLocale alloc] initWithLocaleIdentifier:localeString] autorelease]
                : [NSLocale currentLocale];
            SFSpeechRecognizer *recognizer = [[[SFSpeechRecognizer alloc] initWithLocale:locale] autorelease];
            available = recognizer != nil && [recognizer isAvailable];
        });
        return available;
    }
    return false;
}

bool gokinSpeechStart(uint32_t token, const char *localeValue) {
    if (@available(macOS 14.0, *)) {
        if (!gokinSpeechRuntimeSupported()) {
            return false;
        }
        __block BOOL started = NO;
        NSString *locale = localeValue != NULL ? [NSString stringWithUTF8String:localeValue] : @"";
        gokinOnMainSync(^{
            NSNumber *key = [NSNumber numberWithUnsignedInt:token];
            if ([gokinSpeechControllers() objectForKey:key] != nil) {
                return;
            }
            GokinSpeechController *controller = [[GokinSpeechController alloc] initWithToken:token locale:locale];
            [gokinSpeechControllers() setObject:controller forKey:key];
            [controller release];
            [controller start];
            started = YES;
        });
        return started;
    }
    return false;
}

void gokinSpeechStop(uint32_t token, bool cancel) {
    if (@available(macOS 14.0, *)) {
        gokinOnMainSync(^{
            GokinSpeechController *controller = [gokinSpeechControllers() objectForKey:[NSNumber numberWithUnsignedInt:token]];
            [controller stop:cancel];
        });
    }
}

static void gokinRequestMicrophoneForPermissionToken(uint32_t token, int speechStatus) API_AVAILABLE(macos(14.0));
static void gokinRequestMicrophoneForPermissionToken(uint32_t token, int speechStatus) {
    AVAuthorizationStatus microphone = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
    if (microphone != AVAuthorizationStatusNotDetermined) {
        goNativeSpeechDarwinPermissionResult(token, speechStatus, (int)microphone);
        return;
    }
    [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(__unused BOOL granted) {
        int finalStatus = gokinMicrophoneStatus();
        goNativeSpeechDarwinPermissionResult(token, speechStatus, finalStatus);
    }];
}

void gokinSpeechRequestPermissions(uint32_t token) {
    if (@available(macOS 14.0, *)) {
        gokinOnMainSync(^{
            if (!gokinSpeechRuntimeSupported()) {
                goNativeSpeechDarwinPermissionResult(token, -1, -1);
                return;
            }
            SFSpeechRecognizerAuthorizationStatus speech = [SFSpeechRecognizer authorizationStatus];
            if (speech != SFSpeechRecognizerAuthorizationStatusNotDetermined) {
                gokinRequestMicrophoneForPermissionToken(token, (int)speech);
                return;
            }
            [SFSpeechRecognizer requestAuthorization:^(SFSpeechRecognizerAuthorizationStatus status) {
                gokinRequestMicrophoneForPermissionToken(token, (int)status);
            }];
        });
        return;
    }
    goNativeSpeechDarwinPermissionResult(token, -1, -1);
}
