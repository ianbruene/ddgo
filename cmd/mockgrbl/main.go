package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/ianbruene/ddgo/internal/mockgrbl"
)

func openPTY() (*os.File, string, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, "", err
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCSPTLCK, uintptr(unsafe.Pointer(&[]int32{0}[0]))); errno != 0 {
		_ = unix.Close(fd)
		return nil, "", errno
	}
	var n uint32
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCGPTN, uintptr(unsafe.Pointer(&n))); errno != 0 {
		_ = unix.Close(fd)
		return nil, "", errno
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), fmt.Sprintf("/dev/pts/%d", n), nil
}

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
