// SPDX-License-Identifier: MPL-2.0
//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestBootstrapActualChildCancellationRestoresPTY(t *testing.T) {
	for _, mode := range []string{"raw", "masked"} {
		for _, exit := range []string{"graceful", "forced"} {
			t.Run(mode+"-"+exit, func(t *testing.T) {
				fd, e := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
				if e != nil {
					t.Fatal(e)
				}
				master := os.NewFile(uintptr(fd), "bootstrap-cancel-master")
				defer master.Close()
				if e = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); e != nil {
					t.Fatal(e)
				}
				number, e := unix.IoctlGetInt(fd, unix.TIOCGPTN)
				if e != nil {
					t.Fatal(e)
				}
				slave, e := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
				if e != nil {
					t.Fatal(e)
				}
				defer slave.Close()
				before, e := term.GetState(int(slave.Fd()))
				if e != nil {
					t.Fatal(e)
				}
				ready := filepath.Join(t.TempDir(), "ready")
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBootstrapActualChildFixture$")
				cmd.Env = append(os.Environ(), "RIXA_BOOTSTRAP_CANCEL_MODE="+mode, "RIXA_BOOTSTRAP_CANCEL_EXIT="+exit, "RIXA_BOOTSTRAP_CANCEL_READY="+ready)
				cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, io.Discard, io.Discard
				cmd.SysProcAttr = &unix.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
				done := make(chan error, 1)
				go func() { done <- runBootstrapChild(cmd, slave) }()
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
			waiting:
				for {
					select {
					case e := <-done:
						t.Fatalf("child exited before changing controlling PTY: %v", e)
					case <-deadline.C:
						t.Fatal("child did not enter terminal fixture")
					case <-tick.C:
						if _, e := os.Stat(ready); e == nil {
							break waiting
						}
					}
				}
				changed, e := term.GetState(int(slave.Fd()))
				if e != nil || reflect.DeepEqual(before, changed) {
					t.Fatal("child did not actually change terminal state")
				}
				cancelled := time.Now()
				cancel()
				select {
				case <-done:
				case <-time.After(4 * time.Second):
					t.Fatal("cancelled child was not reaped within the bounded grace/kill window")
				}
				if time.Since(cancelled) > 4*time.Second || cmd.ProcessState == nil {
					t.Fatal("child reap missing")
				}
				after, e := term.GetState(int(slave.Fd()))
				if e != nil || !reflect.DeepEqual(before, after) {
					t.Fatal("original termios was not restored after actual child cancellation")
				}
			})
		}
	}
}
func TestBootstrapActualChildFixture(t *testing.T) {
	mode := os.Getenv("RIXA_BOOTSTRAP_CANCEL_MODE")
	if mode == "" {
		return
	}
	tty, e := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if e != nil {
		t.Fatal("child has no controlling terminal")
	}
	defer tty.Close()
	state, e := term.GetState(int(tty.Fd()))
	if e != nil {
		t.Fatal(e)
	}
	if mode == "raw" {
		_, e = term.MakeRaw(int(tty.Fd()))
	} else {
		value, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
		if err != nil {
			t.Fatal(err)
		}
		value.Lflag &^= unix.ECHO | unix.ECHONL
		e = unix.IoctlSetTermios(int(tty.Fd()), unix.TCSETS, value)
	}
	if e != nil {
		t.Fatal(e)
	}
	if os.Getenv("RIXA_BOOTSTRAP_CANCEL_EXIT") == "forced" {
		signal.Ignore(syscall.SIGTERM)
		if e = os.WriteFile(os.Getenv("RIXA_BOOTSTRAP_CANCEL_READY"), []byte("ready"), 0600); e != nil {
			t.Fatal(e)
		}
		select {}
	}
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, syscall.SIGTERM)
	defer signal.Stop(stopped)
	if e = os.WriteFile(os.Getenv("RIXA_BOOTSTRAP_CANCEL_READY"), []byte("ready"), 0600); e != nil {
		t.Fatal(e)
	}
	<-stopped
	if e = term.Restore(int(tty.Fd()), state); e != nil {
		t.Fatal(e)
	}
}
