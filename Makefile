.all: hole_puncher
.PHONY: install

hole_puncher: hole_puncher.go
	go build hole_puncher.go

install: hole_puncher
	cp hole_puncher /usr/local/bin/containment_breach