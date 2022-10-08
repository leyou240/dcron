package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/leyou240/dcron"
	"github.com/leyou240/dcron/driver/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
)

func main() {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	var e = make([]string, 0)
	e = append(e, "127.0.0.1:2379")
	driver, err := etcd.NewEtcdDriver(&clientv3.Config{Endpoints: e, DialTimeout: time.Second, DialOptions: []grpc.DialOption{grpc.WithBlock()}})
	if err != nil {
		fmt.Println(err)
		return
	}
	cron := dcron.NewDcron("adsa", driver)
	err = cron.AddFunc("", "*/1 * * * *", func() {
		fmt.Println("执行 test1 任务", time.Now().Format("15:04:05"))
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	go func() { cron.Start() }()
	<-interrupt
	fmt.Println("停止任务")
	cron.Stop()
}
