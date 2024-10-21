package bittorrent

import (
	"fmt"
	"os"
)

func AssertExit[T comparable](is, expected T, format string, a ...any) {
	if is != expected {
		fmt.Printf(format, a...)
		os.Exit(1)
	}
}

func AssertNotNil(v any, format string, a ...any) {
	if v != nil {
		fmt.Printf(format, a...)
		os.Exit(1)
	}
}
