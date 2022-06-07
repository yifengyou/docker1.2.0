package dockerversion

// FIXME: this should be embedded in the docker/docker.go,
// but we can't because distro policy requires us to
// package a separate dockerinit binary, and that binary needs
// to know its version too.

var (
	GITCOMMIT string // 通过编译参数注入值
	VERSION   string // 通过编译参数注入值

	IAMSTATIC bool   // whether or not Docker itself was compiled statically via ./hack/make.sh binary
	INITSHA1  string // sha1sum of separate static dockerinit, if Docker itself was compiled dynamically via ./hack/make.sh dynbinary
	INITPATH  string // custom location to search for a valid dockerinit binary (available for packagers as a last resort escape hatch)
)

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