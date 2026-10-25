package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
)

func main() {
	conn, err := net.Dial("tcp", "127.0.0.1:20000")
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	defer conn.Close()
	inputReader := bufio.NewReader(os.Stdin)
	fmt.Println("Please input text to Server:")
	for {
		input, _ := inputReader.ReadString('\n')
		inputInfo := strings.Trim(input, "\r\n")
		if strings.ToUpper(inputInfo) == "Q" {
			return
		}
		_, err := conn.Write([]byte(inputInfo))
		if err != nil {
			fmt.Println("client connect failed,err:", err)
			return
		}
		buf := [512]byte{}
		n, err := conn.Read(buf[:])
		if err != nil {
			fmt.Println("client receive failed,err:", err)
			return
		}
		fmt.Println("the server return is", string(buf[:n]))
	}
}
