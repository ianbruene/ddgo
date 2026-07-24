package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/ianbruene/ddgo/internal/mockgrbl"
)

func main() {
	symlink := flag.String("symlink", "/tmp/ddgo-mock-grbl", "stable symlink path")
	httpAddr := flag.String("http", "127.0.0.1:8088", "debug HTTP address")
	responseDelay := flag.Duration("response-delay", 0, "delay before writing each serial response line")
	suppressResponseFor := flag.String("suppress-response-for", "", "normalized line command whose serial responses should be suppressed")
	holdResponseFor := flag.String("hold-response-for", "", "normalized line command whose serial responses should be held until the mock process exits")
	flag.Parse()
	ctl := mockgrbl.NewController(mockgrbl.DefaultFirmwareProfile(), mockgrbl.DefaultMachineProfile(), nil)
	ptm, slave, err := openPTY()
	if err != nil {
		log.Fatal(err)
	}
	defer ptm.Close()
	_ = os.Remove(*symlink)
	if err := os.Symlink(slave, *symlink); err != nil {
		log.Printf("symlink: %v", err)
	}
	log.Printf("mockgrbl serial path: %s", slave)
	log.Printf("mockgrbl stable path: %s", *symlink)
	go func() { log.Fatal(http.ListenAndServe(*httpAddr, mockgrbl.DebugHandler(ctl))) }()
	writeResponses(ptm, ctl.Connect(), *responseDelay)
	buf := make([]byte, 256)
	for {
		n, err := ptm.Read(buf)
		if n > 0 {
			for _, b := range buf[:n] {
				eventsBefore := len(ctl.Events())
				responses := ctl.ProcessBytes([]byte{b})
				events := ctl.Events()
				newEvents := events[eventsBefore:]
				if shouldSuppressResponses(newEvents, *suppressResponseFor) {
					ctl.DiscardResponseLogs(responses)
					continue
				}
				if shouldHoldResponses(newEvents, *holdResponseFor) {
					log.Printf("holding serial responses for %q until process exit", *holdResponseFor)
					select {}
				}
				writeResponses(ptm, responses, *responseDelay)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				return
			}
			if pathErr, ok := err.(*os.PathError); ok && errors.Is(pathErr.Err, syscall.EIO) {
				continue
			}
			fmt.Fprintln(os.Stderr, err)
			return
		}
	}
}

func writeResponses(w io.Writer, responses []string, delay time.Duration) {
	for _, s := range responses {
		if delay > 0 {
			time.Sleep(delay)
		}
		_, _ = w.Write([]byte(s))
	}
}

func shouldSuppressResponses(events []mockgrbl.LogEntry, command string) bool {
	if command == "" {
		return false
	}
	for _, event := range events {
		if event.Kind == "command" && event.Text == command {
			return true
		}
	}
	return false
}

func shouldHoldResponses(events []mockgrbl.LogEntry, command string) bool {
	if command == "" {
		return false
	}
	for _, event := range events {
		if event.Kind == "command" && event.Text == command {
			return true
		}
	}
	return false
}
