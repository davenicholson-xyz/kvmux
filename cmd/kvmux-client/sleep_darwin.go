//go:build darwin

package main

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/pwr_mgt/IOPMLib.h>
#include <IOKit/IOMessage.h>
#include <CoreFoundation/CoreFoundation.h>
#include <unistd.h>

static io_connect_t          gRootPort;
static IONotificationPortRef gNotifyPort;
static io_object_t           gNotifier;
static int                   gSleepPipeWd = -1;

static void powerCallback(void *ctx, io_service_t svc, natural_t msgType, void *msgArg) {
	if (msgType == kIOMessageSystemWillSleep) {
		if (gSleepPipeWd >= 0) {
			char b = 1;
			write(gSleepPipeWd, &b, 1);
		}
		IOAllowPowerChange(gRootPort, (long)msgArg);
	} else if (msgType == kIOMessageCanSystemSleep) {
		IOAllowPowerChange(gRootPort, (long)msgArg);
	}
}

void startSleepNotifications(int pipeWd) {
	gSleepPipeWd = pipeWd;
	gRootPort = IORegisterForSystemPower(NULL, &gNotifyPort, powerCallback, &gNotifier);
	if (gRootPort == MACH_PORT_NULL) return;
	CFRunLoopAddSource(CFRunLoopGetCurrent(),
	                   IONotificationPortGetRunLoopSource(gNotifyPort),
	                   kCFRunLoopDefaultMode);
	CFRunLoopRun();
}
*/
import "C"

import (
	"log"
	"syscall"
)

var sleepCh = make(chan struct{}, 1)

func startSleepWatcher() {
	var fds [2]int
	if err := syscall.Pipe(fds[:]); err != nil {
		log.Printf("sleep watcher: pipe: %v", err)
		return
	}
	go func() {
		C.startSleepNotifications(C.int(fds[1]))
	}()
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := syscall.Read(fds[0], buf)
			if n > 0 {
				select {
				case sleepCh <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
}
