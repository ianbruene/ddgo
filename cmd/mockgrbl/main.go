package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/ianbruene/ddgo/internal/mockgrbl"
)

func main() {
	symlink := flag.String("symlink", "/tmp/ddgo-mock-grbl", "stable symlink path")
	httpAddr := flag.String("http", "127.0.0.1:8088", "debug HTTP address")
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
	for _, s := range ctl.Connect() {
		_, _ = ptm.Write([]byte(s))
	}
	buf := make([]byte, 256)
	for {
		n, err := ptm.Read(buf)
		if n > 0 {
			for _, s := range ctl.ProcessBytes(buf[:n]) {
				_, _ = ptm.Write([]byte(s))
			}
		}
		if err != nil {
			if err != io.EOF {
				fmt.Fprintln(os.Stderr, err)
			}
			return
		}
	}
}
