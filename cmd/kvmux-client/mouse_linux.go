//go:build linux

package main

import "github.com/go-vgo/robotgo"

func moveMouse(x, y int, dragging bool) {
	robotgo.Move(x, y)
}
