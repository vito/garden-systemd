package main

import "os"

type Request struct {
	Run    *RunRequest
	Attach *AttachRequest
	Signal *SignalRequest
}

type Response struct {
	Run    *RunResponse
	Attach *AttachResponse
	Signal *SignalResponse
	Error  *string
}

type RunRequest struct {
	ID   int
	Path string
	Args []string
	Env  []string
	Dir  string
	TTY  *TTYSpec
}

type TTYSpec struct {
	Columns int
	Rows    int
}

type RunResponse struct{}

type AttachRequest struct {
	ID int
}

type AttachResponse struct{}

type SignalRequest struct {
	ID     int
	Signal os.Signal
}

type SignalResponse struct{}
