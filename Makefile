skeleton: skeleton/bin/wshd

skeleton/bin/wshd: skeleton/bin ginit/* ginit/wshd/*
	CGO_ENABLED=0 go build -a -installsuffix static -o skeleton/bin/wshd ./ginit/wshd

skeleton/bin:
	mkdir -p skeleton/bin
