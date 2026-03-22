//go:build linux

package main

var sleepCh chan struct{} // nil — selecting on a nil channel blocks forever

func startSleepWatcher() {}
