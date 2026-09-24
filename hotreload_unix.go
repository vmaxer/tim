// Completion: 100% - Platform-specific module complete
//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func setupReloadSignal(recompile func(string)) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGUSR1)
	go func() {
		for range sigChan {
			recompile("Manual reload triggered (SIGUSR1)")
		}
	}()
}
