//go:build linux

package main

import "github.com/go-vgo/robotgo"

func moveMouse(x, y int, dragging bool) {
	robotgo.Move(x, y)
}

func mouseButton(button uint16, pressed bool, x, y int) {
	btn := evdevButtonToRobotgo(button)
	if btn == "" {
		return
	}
	if pressed {
		robotgo.MouseDown(btn)
	} else {
		robotgo.MouseUp(btn)
	}
}
