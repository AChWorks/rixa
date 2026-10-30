// SPDX-License-Identifier: MPL-2.0

//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/AChWorks/rixa/internal/serverinstall"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrapUsesControllingTTYWhenStarterInputIsPipe(t *testing.T) {
	fd, e := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if e != nil {
		t.Fatal(e)
	}
	master := os.NewFile(uintptr(fd), "bootstrap-test-ptmx")
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
	receipt := filepath.Join(t.TempDir(), "tty-receipt")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBootstrapControllingTTYChild$")
	cmd.Env = append(os.Environ(), "RIXA_BOOTSTRAP_CTTY_CHILD=1", "RIXA_BOOTSTRAP_CTTY_RECEIPT="+receipt)
	cmd.Stdin = slave
	cmd.SysProcAttr = &unix.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("controlling TTY proof: %v %s", e, out)
	}
	data, e := os.ReadFile(receipt)
	if e != nil || string(data) != "controlling-tty-validated" {
		t.Fatal("controlling TTY handoff was not validated")
	}
}
func TestBootstrapControllingTTYChild(t *testing.T) {
	if os.Getenv("RIXA_BOOTSTRAP_CTTY_CHILD") != "1" {
		return
	}
	pipe, writer, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	defer pipe.Close()
	defer writer.Close()
	backend := &bootstrapTestFacade{}
	opened, launched := false, false
	openTTY := func() (*os.File, error) { opened = true; return os.OpenFile("/dev/tty", os.O_RDWR, 0) }
	launch := func(_ context.Context, r serverinstall.BootstrapRoute, input *os.File, _ io.Writer) error {
		if !opened || input == nil || !term.IsTerminal(int(input.Fd())) || r.ManagerSHA256 != bootstrapTestRoute().ManagerSHA256 {
			return errors.New("controlling terminal or exact manager missing")
		}
		launched = true
		return nil
	}
	if e = bootstrapWithFacade(context.Background(), []string{"menu", "--operation", "retained-update"}, pipe, io.Discard, backend, launch, openTTY); e != nil {
		t.Fatal(e)
	}
	if !launched {
		t.Fatal("verified manager route was not handed off")
	}
	if e = os.WriteFile(os.Getenv("RIXA_BOOTSTRAP_CTTY_RECEIPT"), []byte("controlling-tty-validated"), 0600); e != nil {
		t.Fatal(e)
	}
}
