package main

import (
	"bytes"
	"encoding/gob"
	"flag"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"github.com/vito/garden-systemd/ginit"
)

var runDir = flag.String(
	"run",
	"",
	"directory in which to place the listening socket",
)

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

	mgr := newProcessManager()

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
		var request ginit.Request
		err := dec.Decode(&request)
		if err != nil {
			println("decode: " + err.Error())
			return
		}

		if request.Run != nil {
			mgr.Run(conn, request.Run)
		}

		if request.Attach != nil {
			mgr.Attach(conn, request.Attach)
		}
	}
}

func respondErr(conn net.Conn, err error) {
	msg := err.Error()

	encodeErr := respondUnix(conn, ginit.Response{Error: &msg}, nil)
	if encodeErr != nil {
		println("failed to encode error: " + encodeErr.Error())
	}
}

func respondUnix(conn net.Conn, response ginit.Response, rights []byte) error {
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
