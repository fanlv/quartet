//go:build !windows

package repository

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockPersonalChatFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func syncPersonalChatDir(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
