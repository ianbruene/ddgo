package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	probeOmitResultFor := flag.String("probe-omit-result-for", "", "normalized probe command that should return only ok (test hook)")
	statusPositionField := flag.String("status-position-field", "", "status position field: M, MPos, WPos, or W")
	statusWCORaw := flag.String("status-wco", "", "optional WCO triple: X,Y,Z")
	statusFSRaw := flag.String("status-fs", "", "optional feed/spindle pair: feed,spindle")
	flag.Parse()
	if *statusPositionField != "" && *statusPositionField != "M" && *statusPositionField != "MPos" && *statusPositionField != "WPos" && *statusPositionField != "W" {
		log.Fatalf("invalid -status-position-field %q: want M, MPos, WPos, or W", *statusPositionField)
	}
	statusWCO, statusWCOEnabled, err := parseFloatTripleFlag("status-wco", *statusWCORaw)
	if err != nil {
		log.Fatal(err)
	}
	statusFS, statusFSEnabled, err := parseFloatPairFlag("status-fs", *statusFSRaw)
	if err != nil {
		log.Fatal(err)
	}
	if *statusPositionField == "W" && statusWCOEnabled {
		log.Fatal("-status-position-field W cannot be combined with -status-wco")
	}
	fw := mockgrbl.DefaultFirmwareProfile()
	fw.StatusPositionField = *statusPositionField
	fw.StatusWCO, fw.StatusWCOEnabled = statusWCO, statusWCOEnabled
	fw.StatusFS, fw.StatusFSEnabled = statusFS, statusFSEnabled
	ctl := mockgrbl.NewController(fw, mockgrbl.DefaultMachineProfile(), nil)
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
				if shouldSuppressResponses(newEvents, *probeOmitResultFor) {
					ctl.DiscardResponseLogs(responses)
					writeResponses(ptm, []string{"ok\r\n"}, *responseDelay)
					continue
				}
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

func parseFloatTripleFlag(name, raw string) ([3]float64, bool, error) {
	values, enabled, err := parseFloatListFlag(name, raw, 3)
	if err != nil || !enabled {
		return [3]float64{}, enabled, err
	}
	return [3]float64{values[0], values[1], values[2]}, true, nil
}

func parseFloatPairFlag(name, raw string) ([2]float64, bool, error) {
	values, enabled, err := parseFloatListFlag(name, raw, 2)
	if err != nil || !enabled {
		return [2]float64{}, enabled, err
	}
	return [2]float64{values[0], values[1]}, true, nil
}

func parseFloatListFlag(name, raw string, count int) ([]float64, bool, error) {
	if raw == "" {
		return nil, false, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) != count {
		return nil, false, fmt.Errorf("invalid -%s %q: want %d comma-separated numbers", name, raw, count)
	}
	values := make([]float64, count)
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, false, fmt.Errorf("invalid -%s %q: item %d is not a finite number", name, raw, i+1)
		}
		values[i] = value
	}
	return values, true, nil
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
