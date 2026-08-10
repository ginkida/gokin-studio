//go:build windows

package studio

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

type windowsWakeLease struct {
	stop chan struct{}
	done chan error
	once sync.Once
	err  error
}

func (l *windowsWakeLease) Close() error {
	l.once.Do(func() {
		close(l.stop)
		l.err = <-l.done
	})
	return l.err
}

func wakePlatformSupported() bool { return true }

func acquirePlatformWakeLease(reason string) (wakeLease, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	setThreadExecutionState := kernel32.NewProc("SetThreadExecutionState")
	ready := make(chan error, 1)
	lease := &windowsWakeLease{stop: make(chan struct{}), done: make(chan error, 1)}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		result, _, callErr := setThreadExecutionState.Call(esContinuous | esSystemRequired)
		if result == 0 {
			ready <- fmt.Errorf("SetThreadExecutionState: %v", callErr)
			lease.done <- nil
			return
		}
		ready <- nil
		<-lease.stop
		result, _, callErr = setThreadExecutionState.Call(esContinuous)
		if result == 0 {
			lease.done <- fmt.Errorf("clear SetThreadExecutionState: %v", callErr)
			return
		}
		lease.done <- nil
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return lease, nil
}
