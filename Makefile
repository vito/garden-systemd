all: skeleton

install: skeleton dist/garden-systemd
	rsync dist/garden-systemd /usr/sbin/garden-systemd
	mkdir -p /var/lib/garden-systemd
	rsync -a skeleton/ /var/lib/garden-systemd/skeleton/

dist/garden-systemd: dist * cmd/garden-systemd/*
	go build -o dist/garden-systemd ./cmd/garden-systemd

dist:
	mkdir dist

skeleton: skeleton/bin/wshd

skeleton/bin/wshd: skeleton/bin ginit/* ginit/wshd/*
	CGO_ENABLED=0 go build -a -installsuffix static -o skeleton/bin/wshd ./ginit/wshd

skeleton/bin:
	mkdir -p skeleton/bin
