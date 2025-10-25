package models

import "net"

type Packet struct {
	Payload  []byte
	Addr     *net.UDPAddr
	ID       uint32
	ClientID uint16
	OpCode   byte
	Done     chan bool
	Length   uint16
}

type RawPacket struct {
	Data []byte
	Addr *net.UDPAddr
}
