//go:build !linux

package main

// Landlock is Linux's: anywhere else nothing is confined, so nothing starts.

func landlockABI() int { return 0 }

const minLandlockABI = 6

func restrictAndExec(confineSpec, []string, []string) error { return errNoLandlock }

func landlockReport() string { return "landlock: none" }
