package main

import (
	"os"
	"sync"
	"syscall"

	"github.com/vito/garden-systemd/ginit"
	"github.com/vito/garden-systemd/ptyutil"
)

type Process struct {
	ID uint32

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
	err := ptyutil.SetWinSize(p.StdinW, columns, rows)
	if err != nil {
		return err
	}

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
