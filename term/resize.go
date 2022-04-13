package term

import (
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/remotecommand"
)

func MonitorSize(resize <-chan remotecommand.TerminalSize, initSize remotecommand.TerminalSize) remotecommand.TerminalSizeQueue {
	sizeQueue := &sizeQueue{
		resizeChan: make(chan remotecommand.TerminalSize, 1),
	}

	sizeQueue.monitorSize(resize, initSize)
	return sizeQueue
}

// sizeQueue implements remotecommand.TerminalSizeQueue
type sizeQueue struct {
	// resizeChan receives a Size each time the user's terminal is resized.
	resizeChan chan remotecommand.TerminalSize
}

// make sure sizeQueue implements the resize.TerminalSizeQueue interface
var _ remotecommand.TerminalSizeQueue = &sizeQueue{}

// monitorSize primes resizeChan with initialSizes and then monitors for resize events. With each
// new event, it sends the current terminal size to resizeChan.
func (s *sizeQueue) monitorSize(resize <-chan remotecommand.TerminalSize, initSize remotecommand.TerminalSize) {
	s.resizeChan <- initSize

	// listen for resize events in the background
	go func() {
		defer runtime.HandleCrash()

		for {
			size, ok := <-resize
			if !ok {
				return
			}

			s.resizeChan <- size
		}
	}()
}

// Next returns the new terminal size after the terminal has been resized. It returns nil when
// monitoring has been stopped.
func (s *sizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-s.resizeChan
	if !ok {
		return nil
	}
	return &size
}
