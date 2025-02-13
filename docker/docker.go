package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"github.com/docker/docker/api"
	"github.com/docker/docker/api/client"
	"github.com/docker/docker/dockerversion"
	flag "github.com/docker/docker/pkg/mflag"
	"github.com/docker/docker/reexec"
	"github.com/docker/docker/utils"
)

const (
	// 全局常量，开头小写，限制在当前包内。此处三个常量均只用在当前包的flags文件中
	defaultCaFile   = "ca.pem"
	defaultKeyFile  = "key.pem"
	defaultCertFile = "cert.pem"
)

// docker二进制入口函数，在所有import包的init函数执行完成后才进入main逻辑
func main() {
	// reexec.Init() 函数的定义位于 ./docker/reexec/reexec.go，可以发现由于在docker运行之前没有任何Initializer注册，故该代码段执行的返回值为假。
	// reexec存在的作用是:协调 exec driver 与容器创建时 docker init这两者的关系。
	if reexec.Init() {
		return
	}

	// 解析参数： flag "github.com/docker/flag.go"
	// mflag不同于golang内置库flag，代码里面没有明显应用内置库flag，应该是自己实现的
	flag.Parse()
	// FIXME: validate daemon flags here

	// flVersion为真，输出docker版本信息，并立即结束退出
	if *flVersion {
		// 显示版本信息并退出
		showVersion()
		return
	}
	// flDebug为真，设置DEBUG环境变量为1
	if *flDebug {
		os.Setenv("DEBUG", "1")
	}

	// ftHosts的作用是为 Docker Client 提供所要连接的host对象，也就是为 Docker Server 提供所要监昕的对象。
	if len(flHosts) == 0 {
		// 如果长度是0,说明用户没有传入地址

		// 从环境变量只能中提取DOCKER_HOST参数赋值
		defaultHost := os.Getenv("DOCKER_HOST")

		if defaultHost == "" || *flDaemon {
			// If we do not have a host, default to unix socket

			// 若 defaultHost 为空或者 flDaemon 为真，说明目前还没有一个定义的 host对象，则将其默认设置为 unix socket ，值为 api.DEFAULTUNIXSOCKET ，
			// 该常量位于docker/api/common.go ，值为 "/var/run/docker.sock" ，故 defaultHost 为 "unix:///var/runldocker.sock" 。
			// DEFAULTUNIXSOCKET = "/var/run/docker.sock"
			defaultHost = fmt.Sprintf("unix://%s", api.DEFAULTUNIXSOCKET)
		}
		// 验证该 defaultHost 的合法性之后，将 defaultHost 的值追加至 flHost 的末尾， 继续往下执行。
		if _, err := api.ValidateHost(defaultHost); err != nil {
			log.Fatal(err)
		}
		flHosts = append(flHosts, defaultHost)
	}

	// 若 flDaemon 参数为真，则说明用户的需求是启动 Docker Daemon。
	if *flDaemon {
		mainDaemon()
		// 此处返回，说明后续都是client执行逻辑
		return
	}
	// 若 flHosts 的长度大于 1 ，则说明需要新创建的 Docker Client 访问不止 1 个 Docker Daemon 地址，显然逻辑上行不通，故抛出错误日志，
	// 提醒用户只能指定一个 Docker Daemon 地址。
	// 注意哟，dameon是可以支持多个flHosts的
	// dockerd -H unix:///var/run/docker.sock -H tcp://192.168.59.106 -H tcp://10.10.10.2
	if len(flHosts) > 1 {
		// 致命错误，爆炸退出
		log.Fatal("Please specify only one -H")
	}
	// 获取通过：//分割的两部分
	// "unix:///var/runldocker.sock" -> "/var/runldocker.sock"
	// "tcp://192.168.59.103:2375" -> "192.168.59.103:2375"
	// "fd://3" -> "3"
	protoAddrParts := strings.SplitN(flHosts[0], "://", 2)

	// Docker 在这里创建了两个变量:一个为类型是*c1ient.DockerCli 的对象cli ，另一个为类型是 tls.Config 的对象 tlsConfig 。
	var (
		cli       *client.DockerCli
		tlsConfig tls.Config // TLS协议
	)

	tlsConfig.InsecureSkipVerify = true

	// If we should verify the server, we need to load a trusted ca
	// tlsConfig 对象需要加载一个受信的 ca 文件
	// 如果flTlsVerify为true，Docker Client连接Docker Server需要验证安全性
	if *flTlsVerify {
		*flTls = true
		certPool := x509.NewCertPool()
		file, err := ioutil.ReadFile(*flCa)
		if err != nil {
			log.Fatalf("Couldn't read ca cert %s: %s", *flCa, err)
		}
		certPool.AppendCertsFromPEM(file)
		tlsConfig.RootCAs = certPool
		tlsConfig.InsecureSkipVerify = false
	}

	// If tls is enabled, try to load and send client certificates
	// 如果flTls和flTlsVerify有一个为真，那么需要加载证书发送给客户端。
	if *flTls || *flTlsVerify {
		_, errCert := os.Stat(*flCert)
		_, errKey := os.Stat(*flKey)
		if errCert == nil && errKey == nil {
			*flTls = true
			cert, err := tls.LoadX509KeyPair(*flCert, *flKey)
			if err != nil {
				log.Fatalf("Couldn't load X509 key pair: %s. Key encrypted?", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
	}

	// 创建Docker Client实例句柄
	if *flTls || *flTlsVerify {
		// 实现在./docker/api/client/cli.go
		// 如果flTls或者flTlsVerify为真，那么需要使用TLS保证传输的安全性。
		cli = client.NewDockerCli(os.Stdin, os.Stdout, os.Stderr, protoAddrParts[0], protoAddrParts[1], &tlsConfig)
	} else {
		// 实例化 type DockerCli struct 对象
		cli = client.NewDockerCli(os.Stdin, os.Stdout, os.Stderr, protoAddrParts[0], protoAddrParts[1], nil)
	}

	// 使用 Docker Client实例句柄 执行相应的命令
	// func Args() []string { return CommandLine.args }
	// Args很简单，就是返回字符串列表，因此在参数解析阶段会将合法参数放置才该列表中
	log.Printf("flag.Args:%#v\n", flag.Args())
	// 执行子命令，如果有错误，在这里捕获，如果没有错误，也在这里结束
	// 这里是个典型的路由思想
	if err := cli.Cmd(flag.Args()...); err != nil {
		if sterr, ok := err.(*utils.StatusError); ok {
			if sterr.Status != "" {
				log.Println(sterr.Status)
			}
			os.Exit(sterr.StatusCode)
		}
		log.Fatal(err)
	}
}

func showVersion() {
	fmt.Printf("Docker version %s, build %s\n", dockerversion.VERSION, dockerversion.GITCOMMIT)
}

// dockerversion.VERSION, dockerversion.GITCOMMIT 是通过编译参数赋值
//  go build -o /go/src/github.com/docker/docker/bundles/1.2.0/binary/docker-1.2.0 -a -tags 'netgo static_build apparmor selinux daemon' -ldflags '
//
//	-w
//	-X github.com/docker/docker/dockerversion.GITCOMMIT "0986095-dirty"
//	-X github.com/docker/docker/dockerversion.VERSION "1.2.0"
//
//
//	-linkmode external
//	-X github.com/docker/docker/dockerversion.IAMSTATIC true
//	-extldflags "-static -lpthread -Wl,--unresolved-symbols=ignore-in-object-files"
//
//	' ./docker
