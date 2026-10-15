//go:build !darwin

package main

import "errors"

var errNotMac = errors.New("the server app runs on macOS only; elsewhere use the installer for your system")

func nativeDialogs() (dialogs, error) { return nil, errNotMac }

func optionKeyDown() bool { return false }

func checkDialogs() error { return errNotMac }
