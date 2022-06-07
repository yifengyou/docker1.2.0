// +build !daemon

// golang tag特性
// go bulid -tag 功能来编译不同版本
// go build -tags daemon -o docker

package main

import (
	"log"
)

const CanDaemon = false


// 如果独立编译，则cli没有理由执行daemon函数，此处报错退出
func mainDaemon() {
	log.Fatal("This is a client-only binary - running the Docker daemon is not supported.")
}
