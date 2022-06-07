package main

import (
	"os"
	"path/filepath"

	"github.com/docker/docker/opts"
	flag "github.com/docker/docker/pkg/mflag"
)

var (
	// 尝试从环境变量DOCKER_CERT_PATH获取证书路径
	dockerCertPath = os.Getenv("DOCKER_CERT_PATH")
)

func init() {
	if dockerCertPath == "" {
		// 如果环境变量没有定义证书路径，则从用户家目录下的.docker目录下获取
		dockerCertPath = filepath.Join(os.Getenv("HOME"), ".docker")
	}
}

// 此处仅基本参数，还有其他参数
var (
	flVersion     = flag.Bool([]string{"v", "-version"}, false, "Print version information and quit")
	flDaemon      = flag.Bool([]string{"d", "-daemon"}, false, "Enable daemon mode")
	flDebug       = flag.Bool([]string{"D", "-debug"}, false, "Enable debug mode")
	flSocketGroup = flag.String([]string{"G", "-group"}, "docker", `Group to assign the unix socket specified by -H when running in daemon mode
use '' (the empty string) to disable setting of a group`)
	flEnableCors  = flag.Bool([]string{"#api-enable-cors", "-api-enable-cors"}, false, "Enable CORS headers in the remote API")
	flTls         = flag.Bool([]string{"-tls"}, false, "Use TLS; implied by tls-verify flags")
	flTlsVerify   = flag.Bool([]string{"-tlsverify"}, false, "Use TLS and verify the remote (daemon: verify client, client: verify daemon)")

	// these are initialized in init() below since their default values depend on dockerCertPath which isn't fully initialized until init() runs
	// 先实例化，但是没有赋有效值，默认是类型零值，直到init()中赋值
	flCa    *string
	flCert  *string
	flKey   *string
	flHosts []string
)

/*
Usage of ./docker-1.2.0:
  --api-enable-cors=false                Enable CORS headers in the remote API
  -b, --bridge=""                        Attach containers to a pre-existing network bridge
                                           use 'none' to disable container networking
  --bip=""                               Use this CIDR notation address for the network bridge's IP, not compatible with -b
  -D, --debug=false                      Enable debug mode
  -d, --daemon=false                     Enable daemon mode
  --dns=[]                               Force Docker to use specific DNS servers
  --dns-search=[]                        Force Docker to use specific DNS search domains
  -e, --exec-driver="native"             Force the Docker runtime to use a specific exec driver
  -G, --group="docker"                   Group to assign the unix socket specified by -H when running in daemon mode
                                           use '' (the empty string) to disable setting of a group
  -g, --graph="/var/lib/docker"          Path to use as the root of the Docker runtime
  -H, --host=[]                          The socket(s) to bind to in daemon mode
                                           specified using one or more tcp://host:port, unix:///path/to/socket, fd://* or fd://socketfd.
  --icc=true                             Enable inter-container communication
  --ip=0.0.0.0                           Default IP address to use when binding container ports
  --ip-forward=true                      Enable net.ipv4.ip_forward
  --iptables=true                        Enable Docker's addition of iptables rules
  --mtu=0                                Set the containers network MTU
                                           if no value is provided: default to the default route MTU or 1500 if no default route is available
  -p, --pidfile="/var/run/docker.pid"    Path to use for daemon PID file
  -s, --storage-driver=""                Force the Docker runtime to use a specific storage driver
  --selinux-enabled=false                Enable selinux support. SELinux does not presently support the BTRFS storage driver
  --storage-opt=[]                       Set storage driver options
  --tls=false                            Use TLS; implied by tls-verify flags
  --tlscacert="/root/.docker/ca.pem"     Trust only remotes providing a certificate signed by the CA given here
  --tlscert="/root/.docker/cert.pem"     Path to TLS certificate file
  --tlskey="/root/.docker/key.pem"       Path to TLS key file
  --tlsverify=false                      Use TLS and verify the remote (daemon: verify client, client: verify daemon)
  -v, --version=false                    Print version information and quit
*/


// 同一个go程序可以包含多个init函数，但是执行顺序没法保证
func init() {
	flCa = flag.String([]string{"-tlscacert"}, filepath.Join(dockerCertPath, defaultCaFile), "Trust only remotes providing a certificate signed by the CA given here")
	flCert = flag.String([]string{"-tlscert"}, filepath.Join(dockerCertPath, defaultCertFile), "Path to TLS certificate file")
	flKey = flag.String([]string{"-tlskey"}, filepath.Join(dockerCertPath, defaultKeyFile), "Path to TLS key file")
	// opts是mflag的封装，
	opts.HostListVar(&flHosts, []string{"H", "-host"}, `The socket(s) to bind to in daemon mode
specified using one or more tcp://host:port, unix:///path/to/socket, fd://* or fd://socketfd.`)
}
