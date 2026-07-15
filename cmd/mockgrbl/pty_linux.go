//go:build linux

package main

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

func openPTY() (*os.File, string, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, "", err
	}
	unlock := int32(0)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		_ = unix.Close(fd)
		return nil, "", errno
	}
	var n uint32
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCGPTN, uintptr(unsafe.Pointer(&n))); errno != 0 {
		_ = unix.Close(fd)
		return nil, "", errno
	}
	slave := fmt.Sprintf("/dev/pts/%d", n)
	if err := configureRawSlave(slave); err != nil {
		_ = unix.Close(fd)
		return nil, "", err
	}
	return os.NewFile(uintptr(fd), "/dev/ptmx"), slave, nil
}

func configureRawSlave(path string) error {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	termios, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	termios.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	termios.Oflag &^= unix.OPOST
	termios.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	termios.Cflag &^= unix.CSIZE | unix.PARENB
	termios.Cflag |= unix.CS8
	termios.Cc[unix.VMIN] = 1
	termios.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(fd, unix.TCSETS, termios)
}
