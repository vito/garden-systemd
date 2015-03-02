package main

import (
	"bytes"
	"encoding/gob"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
)

var socketPath = flag.String(
	"socket",
	"",
	"socket of the wshd to communicate with",
)

var id = flag.Int(
	"id",
	0,
	"id of the process, for reattaching later",
)

var dir = flag.String(
	"dir",
	"/",
	"working directory for the process to spawn",
)

var tty = flag.Bool(
	"tty",
	false,
	"spawn process with a tty",
)

var windowColumns = flag.Int(
	"windowColumns",
	80,
	"initial window columns for the process's tty",
)

var windowRows = flag.Int(
	"windowRows",
	24,
	"initial window rows for the process's tty",
)

func main() {
	flag.Parse()

	if len(*socketPath) == 0 {
		println("must specify -socket")
		os.Exit(255)
	}

	argv := flag.Args()

	conn, err := net.Dial("unix", *socketPath)
	if err != nil {
		println("dial wshd: " + err.Error())
		os.Exit(255)
	}

	enc := gob.NewEncoder(conn)

	runRequest := &RunRequest{
		ID:   *id,
		Path: argv[0],
		Args: argv[1:],
		Dir:  *dir,
		Env:  os.Environ(),
	}

	if *tty {
		runRequest.TTY = &TTYSpec{
			Columns: *windowColumns,
			Rows:    *windowRows,
		}
	}

	err = enc.Encode(Request{
		Run: runRequest,
	})
	if err != nil {
		println("run request: " + err.Error())
		os.Exit(255)
	}

	var b [2048]byte
	var oob [2048]byte

	n, oobn, _, _, err := conn.(*net.UnixConn).ReadMsgUnix(b[:], oob[:])
	if err != nil {
		println("read unix msg: " + err.Error())
		os.Exit(255)
	}

	var response Response
	err = gob.NewDecoder(bytes.NewBuffer(b[:n])).Decode(&response)
	if err != nil {
		println("decode response: " + err.Error())
		os.Exit(255)
	}

	if response.Error != nil {
		println("remote error: " + *response.Error)
		os.Exit(255)
	}

	scms, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		println("parse socket control msg: " + err.Error())
		os.Exit(255)
	}

	if len(scms) < 1 {
		println("no socket control messages received")
		os.Exit(255)
	}

	scm := scms[0]

	fds, err := syscall.ParseUnixRights(&scm)
	if err != nil {
		println("parse unix rights: " + err.Error())
		os.Exit(255)
	}

	if len(fds) != 4 {
		println(fmt.Sprintf("expected 4 fds, got %d", len(fds)))
		os.Exit(255)
	}

	lstdin := os.NewFile(uintptr(fds[0]), "stdin")
	lstdout := os.NewFile(uintptr(fds[1]), "stdout")
	lstderr := os.NewFile(uintptr(fds[2]), "stderr")
	lstatus := os.NewFile(uintptr(fds[3]), "status")

	copying := new(sync.WaitGroup)

	go func() {
		io.Copy(lstdin, os.Stdin)
		lstdin.Close()
	}()

	copying.Add(1)
	go func() {
		io.Copy(os.Stdout, lstdout)
		lstdout.Close()
		copying.Done()
	}()

	if !*tty {
		copying.Add(1)
		go func() {
			io.Copy(os.Stderr, lstderr)
			lstderr.Close()
			copying.Done()
		}()
	}

	var status int
	_, err = fmt.Fscanf(lstatus, "%d", &status)
	if err != nil {
		println("read status: " + err.Error())
		os.Exit(255)
	}

	copying.Wait()

	os.Exit(status)
}
