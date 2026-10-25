// package main
//
// import (
// 	"fmt"
// )
//
// func main() {
// 	var ch chan int
// 	fmt.Println(ch)
// 	ch1 := make(chan int)
// 	go func() { ch1 <- 10 }()
// 	// 发送接收不能在同一个goroutine，否则会死锁
// 	x := <-ch1
// 	fmt.Println(x)
// }
