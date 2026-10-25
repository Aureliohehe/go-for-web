package main

import (
	"fmt"
	"net"
)

func main() {
	socket, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.IPv4(0, 0, 0, 0),
		Port: 30000,
	})
	if err != nil {
		fmt.Println("connect failed,err:", err)
		return
	}
	defer socket.Close()
	sendData := []byte("hello server")
	_, err = socket.Write(sendData)
	if err != nil {
		fmt.Println("send data failed,err:", err)
		return
	}
	data := make([]byte, 4096)
	n, remoteAddr, err := socket.ReadFromUDP(data)
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	fmt.Println("recv:%v addr:%v count:%v\n", string(data[:n]), remoteAddr, n)
}
