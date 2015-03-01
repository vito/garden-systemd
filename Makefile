skeleton: skeleton/bin/iodaemon skeleton/bin/wsh skeleton/bin/wshd

skeleton/bin/iodaemon: skeleton/bin iodaemon/**/*
	go build -o skeleton/bin/iodaemon ./iodaemon/

skeleton/bin/wshd: skeleton/bin init/wshd
	cd init && make
	cp init/wshd skeleton/bin/wshd

skeleton/bin/wsh: skeleton/bin init/wsh
	cp init/wsh skeleton/bin/wsh

init/wshd: init/*.c
	cd init && make wshd

init/wsh: init/*.c
	cd init && make wsh

skeleton/bin:
	mkdir -p skeleton/bin
