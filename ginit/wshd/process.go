package main

import (
	"os"
	"strconv"
	"sync"
	"syscall"

	"github.com/vito/garden-systemd/ginit"
	"github.com/vito/garden-systemd/ptyutil"
)

type Process struct {
	ID string

	Process *os.Process

	StatusR *os.File
	StdinW  *os.File
	StdoutR *os.File
	StderrR *os.File

	lock sync.Mutex
}

func (p *Process) Rights() ginit.FDRights {
	return ginit.FDRights{
		Status: fdRef(p.StatusR),
		Stdin:  fdRef(p.StdinW),
		Stdout: fdRef(p.StdoutR),
		Stderr: fdRef(p.StderrR),
	}
}

func (p *Process) CloseStdin() error {
	err := p.StdinW.Close()
	if err != nil {
		return err
	}

	p.StdinW = nil

	return nil
}

func (p *Process) SetWindowSize(columns, rows int) error {
	println("updating pty size to: " + strconv.Itoa(columns) + "x" + strconv.Itoa(rows))

	err := ptyutil.SetWinSize(p.StdinW, columns, rows)
	if err != nil {
		return err
	}

	println("sending SIGWINCH to " + strconv.Itoa(p.Process.Pid))

	return p.Process.Signal(syscall.SIGWINCH)
}

func (p *Process) Signal(signal os.Signal) error {
	return p.Process.Signal(signal)
}

func fdRef(file *os.File) *int {
	if file == nil {
		return nil
	}

	fd := int(file.Fd())
	return &fd
}
