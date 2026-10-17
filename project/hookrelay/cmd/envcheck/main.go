package main

import (
	"fmt"
	"os"
)

func main() {
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		fmt.Println("NO read MYSQL_HOST")
		os.Exit(1)
	}
	fmt.Printf("database host:%s\n", host)
}
