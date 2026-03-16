//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <ApplicationServices/ApplicationServices.h>
#include <time.h>

static struct timespec gLastClickTime[3];
static CGPoint        gLastClickPos[3];
static int            gClickCount[3];

static double msElapsed(struct timespec a, struct timespec b) {
	return (double)(a.tv_sec  - b.tv_sec)  * 1000.0
	     + (double)(a.tv_nsec - b.tv_nsec) / 1000000.0;
}

void kvmMoveMouse(int x, int y, int dragging) {
	CGPoint pos = CGPointMake(x, y);
	CGEventType evType = dragging ? kCGEventLeftMouseDragged : kCGEventMouseMoved;
	CGEventRef ev = CGEventCreateMouseEvent(NULL, evType, pos, kCGMouseButtonLeft);
	CGEventPost(kCGHIDEventTap, ev);
	CFRelease(ev);
}

// btnIdx: 0=left, 1=right, 2=middle
void kvmMouseButton(int btnIdx, int pressed, int x, int y) {
	CGMouseButton cgBtn;
	CGEventType   downType, upType;
	switch (btnIdx) {
	case 0:
		cgBtn    = kCGMouseButtonLeft;
		downType = kCGEventLeftMouseDown;
		upType   = kCGEventLeftMouseUp;
		break;
	case 1:
		cgBtn    = kCGMouseButtonRight;
		downType = kCGEventRightMouseDown;
		upType   = kCGEventRightMouseUp;
		break;
	default:
		cgBtn    = kCGMouseButtonCenter;
		downType = kCGEventOtherMouseDown;
		upType   = kCGEventOtherMouseUp;
		break;
	}

	CGPoint pos = CGPointMake(x, y);

	if (pressed) {
		struct timespec now;
		clock_gettime(CLOCK_MONOTONIC, &now);

		double elapsed = msElapsed(now, gLastClickTime[btnIdx]);
		double ddx = x - gLastClickPos[btnIdx].x;
		double ddy = y - gLastClickPos[btnIdx].y;
		// Same threshold macOS uses: ~500 ms, ~5 px radius.
		if (elapsed < 500.0 && (ddx*ddx + ddy*ddy) < 25.0) {
			gClickCount[btnIdx]++;
		} else {
			gClickCount[btnIdx] = 1;
		}
		gLastClickTime[btnIdx] = now;
		gLastClickPos[btnIdx]  = pos;

		CGEventRef ev = CGEventCreateMouseEvent(NULL, downType, pos, cgBtn);
		CGEventSetIntegerValueField(ev, kCGMouseEventClickState, gClickCount[btnIdx]);
		CGEventPost(kCGHIDEventTap, ev);
		CFRelease(ev);
	} else {
		CGEventRef ev = CGEventCreateMouseEvent(NULL, upType, pos, cgBtn);
		CGEventSetIntegerValueField(ev, kCGMouseEventClickState, gClickCount[btnIdx]);
		CGEventPost(kCGHIDEventTap, ev);
		CFRelease(ev);
	}
}
*/
import "C"

func moveMouse(x, y int, dragging bool) {
	d := C.int(0)
	if dragging {
		d = 1
	}
	C.kvmMoveMouse(C.int(x), C.int(y), d)
}

func mouseButton(button uint16, pressed bool, x, y int) {
	btnIdx := evdevButtonIndex(button)
	if btnIdx < 0 {
		return
	}
	p := C.int(0)
	if pressed {
		p = 1
	}
	C.kvmMouseButton(C.int(btnIdx), p, C.int(x), C.int(y))
}

func evdevButtonIndex(code uint16) int {
	switch code {
	case 0x110:
		return 0 // left
	case 0x111:
		return 1 // right
	case 0x112:
		return 2 // middle
	}
	return -1
}
