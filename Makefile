skeleton: skeleton/bin/wsh skeleton/bin/wshd

skeleton/bin/wshd: skeleton/bin init/wshd.go init/msg.go
	CGO_ENABLED=0 go build -a -installsuffix static -o skeleton/bin/wshd ./init/wshd.go ./init/msg.go

skeleton/bin/wsh: skeleton/bin init/wsh.go init/msg.go
	CGO_ENABLED=0 go build -a -installsuffix static -o skeleton/bin/wsh ./init/wsh.go ./init/msg.go

skeleton/bin:
	mkdir -p skeleton/bin
