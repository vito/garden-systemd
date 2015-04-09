package ginit

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

type Request struct {
	Run           *RunRequest
	Attach        *AttachRequest
	Signal        *SignalRequest
	CreateDir     *CreateDirRequest
	SetWindowSize *SetWindowSizeRequest
}

type Response struct {
	Run           *RunResponse
	Attach        *AttachResponse
	Signal        *SignalResponse
	CreateDir     *CreateDirResponse
	SetWindowSize *SetWindowSizeResponse
	Error         *string
}

type RunRequest struct {
	Path string
	Args []string
	Env  []string
	Dir  string
	TTY  *TTYSpec
	User string
}

type TTYSpec struct {
	Columns int
	Rows    int
}

type FDRights struct {
	Status *int // should always be present
	Stdin  *int // will not be present if already closed
	Stdout *int // should always be present
	Stderr *int // will not be present with tty
}

type FDOffsets struct {
	Status *int
	Stdin  *int
	Stdout *int
	Stderr *int
}

func (off FDOffsets) GoString() string {
	offsets := []string{}

	if off.Status != nil {
		offsets = append(offsets, fmt.Sprintf("&%d", *off.Status))
	} else {
		offsets = append(offsets, "nil")
	}

	if off.Stdin != nil {
		offsets = append(offsets, fmt.Sprintf("&%d", *off.Stdin))
	} else {
		offsets = append(offsets, "nil")
	}

	if off.Stdout != nil {
		offsets = append(offsets, fmt.Sprintf("&%d", *off.Stdout))
	} else {
		offsets = append(offsets, "nil")
	}

	if off.Stderr != nil {
		offsets = append(offsets, fmt.Sprintf("&%d", *off.Stderr))
	} else {
		offsets = append(offsets, "nil")
	}

	return "FDOffsets{" + strings.Join(offsets, ", ") + "}"
}

func ref(x int) *int {
	return &x
}

func (rights FDRights) Offsets() FDOffsets {
	offsets := FDOffsets{}

	o := 0
	if rights.Status != nil {
		offsets.Status = ref(o)
		o++
	}

	if rights.Stdin != nil {
		offsets.Stdin = ref(o)
		o++
	}

	if rights.Stdout != nil {
		offsets.Stdout = ref(o)
		o++
	}

	if rights.Stderr != nil {
		offsets.Stderr = ref(o)
		o++
	}

	return offsets
}

func (rights FDRights) UnixRights() []byte {
	fds := []int{}

	if rights.Status != nil {
		fds = append(fds, *rights.Status)
	}

	if rights.Stdin != nil {
		fds = append(fds, *rights.Stdin)
	}

	if rights.Stdout != nil {
		fds = append(fds, *rights.Stdout)
	}

	if rights.Stderr != nil {
		fds = append(fds, *rights.Stderr)
	}

	return syscall.UnixRights(fds...)
}

type RunResponse struct {
	ProcessID uint32
	Rights    FDRights
}

type AttachRequest struct {
	ProcessID uint32
}

type AttachResponse struct {
	Rights FDRights
}

type SignalRequest struct {
	ProcessID uint32
	Signal    os.Signal
}

type SignalResponse struct{}

type SetWindowSizeRequest struct {
	ProcessID uint32
	Columns   int
	Rows      int
}

type SetWindowSizeResponse struct{}

type CreateDirRequest struct {
	Path string
}

type CreateDirResponse struct{}
