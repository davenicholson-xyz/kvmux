//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <ApplicationServices/ApplicationServices.h>

void kvmMoveMouse(int x, int y, int dragging) {
	CGPoint pos = CGPointMake(x, y);
	CGEventType evType = dragging ? kCGEventLeftMouseDragged : kCGEventMouseMoved;
	CGEventRef ev = CGEventCreateMouseEvent(NULL, evType, pos, kCGMouseButtonLeft);
	CGEventPost(kCGHIDEventTap, ev);
	CFRelease(ev);
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
