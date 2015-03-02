package main

import (
	"bytes"
	"encoding/gob"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/kr/pty"
	"github.com/vito/garden-systemd/ptyutil"
)

var runDir = flag.String(
	"run",
	"",
	"directory in which to place the listening socket",
)

type Process struct {
	ID int

	Process *os.Process

	StdinW  *os.File
	StdoutR *os.File
	StderrR *os.File
	StatusR *os.File
}

type ProcessManager struct {
	Processes  map[int]*Process
	processesL sync.Mutex
}

func main() {
	flag.Parse()

	if len(*runDir) == 0 {
		println("must specify -run")
		os.Exit(1)
	}

	socketPath := filepath.Join(*runDir, "wshd.sock")

	err := os.RemoveAll(socketPath)
	if err != nil {
		println("remove existing socket: " + err.Error())
		os.Exit(1)
	}

	sock, err := net.Listen("unix", socketPath)
	if err != nil {
		println("listen: " + err.Error())
		os.Exit(1)
	}

	err = syscall.Unmount(*runDir, syscall.MNT_DETACH)
	if err != nil {
		println("umount run dir: " + err.Error())
		os.Exit(1)
	}

	err = os.RemoveAll(*runDir)
	if err != nil {
		println("cleanup run dir: " + err.Error())
		os.Exit(1)
	}

	mgr := &ProcessManager{
		Processes: make(map[int]*Process),
	}

	for {
		conn, err := sock.Accept()
		if err != nil {
			println("accept: " + err.Error())
			os.Exit(1)
		}

		go handleConnection(mgr, conn)
	}
}

func handleConnection(mgr *ProcessManager, conn net.Conn) {
	dec := gob.NewDecoder(conn)

	for {
		var request Request
		err := dec.Decode(&request)
		if err != nil {
			println("decode: " + err.Error())
			return
		}

		if request.Run != nil {
			mgr.Run(conn, request.Run)
		}
	}
}

func (mgr *ProcessManager) Run(conn net.Conn, req *RunRequest) {
	bin, err := exec.LookPath(req.Path)
	if err != nil {
		println("path lookup: " + err.Error())
		respondErr(conn, err)
		return
	}

	cmd := &exec.Cmd{
		Path: bin,
		Args: append([]string{req.Path}, req.Args...),
		Dir:  req.Dir,
		Env:  req.Env,
	}

	// stderr will not be assigned in the case of a tty, so make
	// a dummy pipe to send across instead
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		println("create stderr pipe: " + err.Error())
		respondErr(conn, err)
		return
	}

	var stdinW, stdoutR *os.File
	var stdinR, stdoutW *os.File

	if req.TTY != nil {
		pty, tty, err := pty.Open()
		if err != nil {
			println("create pty: " + err.Error())
			respondErr(conn, err)
			return
		}

		// do NOT assign stderrR to pty; the receiving end should only receive one
		// pty output stream, as they're both the same fd

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

	statusR, statusW, err := os.Pipe()
	if err != nil {
		println("create status pipe: " + err.Error())
		respondErr(conn, err)
		return
	}

	rights := syscall.UnixRights(
		int(stdinW.Fd()),
		int(stdoutR.Fd()),
		int(stderrR.Fd()),
		int(statusR.Fd()),
	)

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

	mgr.processesL.Lock()
	mgr.Processes[req.ID] = &Process{
		ID: req.ID,

		Process: cmd.Process,

		StdinW:  stdinW,
		StdoutR: stdoutR,
		StderrR: stderrR,
		StatusR: statusR,
	}
	mgr.processesL.Unlock()

	err = respondUnix(conn, Response{Run: &RunResponse{}}, rights)
	if err != nil {
		println("failed to encode response: " + err.Error())
		return
	}

	// TODO: closing stdin should be an explicit message that results in
	// this.
	stdinW.Close()
}

func respondErr(conn net.Conn, err error) {
	msg := err.Error()

	encodeErr := respondUnix(conn, Response{Error: &msg}, nil)
	if encodeErr != nil {
		println("failed to encode error: " + encodeErr.Error())
	}
}

func respondUnix(conn net.Conn, response Response, rights []byte) error {
	respBuf := new(bytes.Buffer)

	err := gob.NewEncoder(respBuf).Encode(response)
	if err != nil {
		return err
	}

	_, _, err = conn.(*net.UnixConn).WriteMsgUnix(respBuf.Bytes(), rights, nil)
	if err != nil {
		return err
	}

	return nil
}
