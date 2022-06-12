// +build daemon

// golang tag特性
// go bulid -tag 功能来编译不同版本
// go build -tags daemon -o docker

package main

import (
	"log"

	"github.com/docker/docker/builtins"
	"github.com/docker/docker/daemon"
	_ "github.com/docker/docker/daemon/execdriver/lxc"
	_ "github.com/docker/docker/daemon/execdriver/native"
	"github.com/docker/docker/dockerversion"
	"github.com/docker/docker/engine"
	flag "github.com/docker/docker/pkg/mflag"
	"github.com/docker/docker/pkg/signal"
)

const CanDaemon = true

var (
	// 先于 init 函数
	daemonCfg = &daemon.Config{}
)

func init() {
	// 在mainDaemon之前执行，daemon特供参数解析
	daemonCfg.InstallFlags()
}

func mainDaemon() {
	// 除了已经解析的，还剩下的无法解析的参数，还有至少一个，则打印帮助信息并退出
	if flag.NArg() != 0 {
		flag.Usage()
		return
	}
	// 初始化dameon中的关键模块engine
	// docker daemon = eng + server
	// engine 先实例化
	eng := engine.New()
	// 处理信号，封装了SIGINT\SIGTERM\SIGQUIT，优雅退出
	signal.Trap(eng.Shutdown)
	// Load builtins 注册内置操作句柄到引擎中，与容器交互无关
	if err := builtins.Register(eng); err != nil {
		log.Fatal(err)
	}

	// 运行docker server，该server仅与docker client交互
	// load the daemon in the background so we can immediately start
	// the http api so that connections don't fail while the daemon
	// is booting
	go func() {
		// 实例化daemon，实际上 daemon = server + engine
		d, err := daemon.NewDaemon(daemonCfg, eng) // 重点，内容很深
		if err != nil {
			log.Fatal(err)
		}
		// 注册操作容器、镜像的job句柄
		if err := d.Install(eng); err != nil {
			log.Fatal(err)
		}
		// after the daemon is done setting up we can tell the api to start
		// accepting connections
		// 通知systemd，服务启动完毕，可以接收请求
		if err := eng.Job("acceptconnections").Run(); err != nil {
			log.Fatal(err)
		}
	}()
	// TODO actually have a resolved graphdriver to show?
	// 2022/06/07 07:21:40 docker daemon: 1.2.0 908feb4-dirty; execdriver: native; graphdriver:
	log.Printf("docker daemon: %s %s; execdriver: %s; graphdriver: %s",
		dockerversion.VERSION,
		dockerversion.GITCOMMIT,
		daemonCfg.ExecDriver,
		daemonCfg.GraphDriver,
	)

	// Serve api 初始化serverapi job，还未运行
	// func ServeApi(job *engine.Job) engine.Status
	// serveapi 开始服务需要在 acceptconnections job 关闭chan开始
	job := eng.Job("serveapi", flHosts...)
	job.SetenvBool("Logging", true)
	job.SetenvBool("EnableCors", *flEnableCors)
	job.Setenv("Version", dockerversion.VERSION)
	job.Setenv("SocketGroup", *flSocketGroup)

	job.SetenvBool("Tls", *flTls)
	job.SetenvBool("TlsVerify", *flTlsVerify)
	job.Setenv("TlsCa", *flCa)
	job.Setenv("TlsCert", *flCert)
	job.Setenv("TlsKey", *flKey)
	job.SetenvBool("BufferRequests", true)
	// 运行job
	if err := job.Run(); err != nil {
		log.Fatal(err)
	}
}
