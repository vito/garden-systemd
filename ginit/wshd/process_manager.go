package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/kr/pty"
	"github.com/vito/garden-systemd/ginit"
	"github.com/vito/garden-systemd/ptyutil"
)

func newProcessManager() *ProcessManager {
	return &ProcessManager{
		nextProcessID: 1,

		processes: make(map[uint32]*Process),
	}
}

type ProcessManager struct {
	nextProcessID uint32

	processes  map[uint32]*Process
	processesL sync.Mutex
}

func (mgr *ProcessManager) Run(conn net.Conn, req *ginit.RunRequest) {
	var execPath string
	if strings.Contains(req.Path, "/") {
		execPath = req.Path
	} else {
		bin, err := exec.LookPath(req.Path)
		if err != nil {
			println("path lookup: " + err.Error())
			respondErr(conn, err)
			return
		}

		execPath = bin
	}

	cmd := &exec.Cmd{
		Path: execPath,
		Args: append([]string{req.Path}, req.Args...),
		Dir:  req.Dir,
		Env:  req.Env,
	}

	statusR, statusW, err := os.Pipe()
	if err != nil {
		println("create status pipe: " + err.Error())
		respondErr(conn, err)
		return
	}

	// stderr will not be assigned in the case of a tty, so make
	// a dummy pipe to send across instead
	var stdinR, stdinW *os.File
	var stdoutR, stdoutW *os.File
	var stderrR, stderrW *os.File

	if req.TTY != nil {
		pty, tty, err := pty.Open()
		if err != nil {
			println("create pty: " + err.Error())
			respondErr(conn, err)
			return
		}

		// do NOT assign stderrR to pty; the receiving end should only
		// receive one pty output stream, as they're both the same fd

		stdinW = pty
		stdoutR = pty

		stdinR = tty
		stdoutW = tty
		stderrW = tty

		ptyutil.SetWinSize(stdinW, req.TTY.Columns, req.TTY.Rows)

		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setctty: true,
			Setsid:  true,
		}
	} else {
		stderrR, stderrW, err = os.Pipe()
		if err != nil {
			println("create stderr pipe: " + err.Error())
			respondErr(conn, err)
			return
		}

		stdinR, stdinW, err = os.Pipe()
		if err != nil {
			println("create stdin pipe: " + err.Error())
			respondErr(conn, err)
			return
		}

		stdoutR, stdoutW, err = os.Pipe()
		if err != nil {
			println("create stdout pipe: " + err.Error())
			respondErr(conn, err)
			return
		}
	}

	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	err = cmd.Start()
	if err != nil {
		println("start: " + err.Error())
		respondErr(conn, err)
		return
	}

	// close no longer relevant pipe ends
	// this closes tty 3 times but that's OK
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()

	go func() {
		err := cmd.Wait()
		if err != nil {
			println("wait: " + err.Error())
		}

		if cmd.ProcessState != nil {
			fmt.Fprintf(statusW, "%d\n", cmd.ProcessState.Sys().(syscall.WaitStatus).ExitStatus())
		}
	}()

	process := &Process{
		Process: cmd.Process,

		StdinW:  stdinW,
		StdoutR: stdoutR,
		StderrR: stderrR,
		StatusR: statusR,
	}

	mgr.processesL.Lock()
	process.ID = mgr.nextProcessID
	mgr.nextProcessID++
	mgr.processes[process.ID] = process
	mgr.processesL.Unlock()

	rights := process.Rights()

	err = respondUnix(
		conn,
		ginit.Response{
			Run: &ginit.RunResponse{
				ProcessID: process.ID,
				Rights:    rights,
			},
		},
		rights.UnixRights(),
	)
	if err != nil {
		println("failed to encode response: " + err.Error())
		return
	}

	// TODO: closing stdin should be an explicit message that results in
	// this.
	process.CloseStdin()
}

func (mgr *ProcessManager) Attach(conn net.Conn, req *ginit.AttachRequest) {
	mgr.processesL.Lock()
	process, found := mgr.processes[req.ProcessID]
	mgr.processesL.Unlock()

	if !found {
		respondErr(conn, fmt.Errorf("unknown process: %d", req.ProcessID))
		return
	}

	rights := process.Rights()

	err := respondUnix(
		conn,
		ginit.Response{
			Attach: &ginit.AttachResponse{
				Rights: rights,
			},
		},
		rights.UnixRights(),
	)
	if err != nil {
		println("failed to encode response: " + err.Error())
		return
	}
}
