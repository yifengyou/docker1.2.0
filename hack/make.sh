#!/usr/bin/env bash
set -xe

# This script builds various binary artifacts from a checkout of the docker
# source code.
#
# Requirements:
# - The current directory should be a checkout of the docker source code
#   (http://github.com/docker/docker). Whatever version is checked out
#   will be built.
# - The VERSION file, at the root of the repository, should exist, and
#   will be used as Docker binary version and package version.
#   VERSION文件必须存在于源码根路径，作为输出二进制的版本
# - The hash of the git commit will also be included in the Docker binary,
#   with the suffix -dirty if the repository isn't clean.
# - The script is intented to be run inside the docker container specified
#   in the Dockerfile at the root of the source. In other words:
#   DO NOT CALL THIS SCRIPT DIRECTLY. 不要直接调用，除非你清楚自己在做什么
# - The right way to call this script is to invoke "make" from
#   your checkout of the Docker repository.
#   the Makefile will do a "docker build -t docker ." and then
#   "docker run hack/make.sh" in the resulting image.
#   构建环境本身也是一个容器，鸡生蛋，蛋生鸡

set -o pipefail

# 对于set命令-o参数的pipefail选项，linux是这样解释的：
#"If set, the return value of a pipeline is the value of the last (rightmost)
# command to exit with a  non-zero  status,or zero if all commands in the
# pipeline exit successfully.  This option is disabled by default."
#
# 设置了这个选项以后，包含管道命令的语句的返回值，会变成最后一个返回非零的管道命令的返回值
# 对于管道中执行非0的情况，将作为返回值。而不是管道之后的执行返回值
## test.sh
# set -o pipefail
# ls ./a.txt |echo "hi" >/dev/null
#
#运行test.sh，因为当前目录并不存在a.txt文件，输出：
#ls: ./a.txt: No such file or directory
#1  # 设置了set -o pipefail，返回从右往左第一个非零返回值，即ls的返回值1
#
#注释掉set -o pipefail 这一行，再次运行，输出：
#ls: ./a.txt: No such file or directory
#0  # 没有set -o pipefail，默认返回最后一个管道命令的返回值


export DOCKER_PKG='github.com/docker/docker'

# We're a nice, sexy, little shell script, and people might try to run us;
# but really, they shouldn't. We want to be in a container!
# 强烈建议在容器中运行
if [ "$(pwd)" != "/go/src/$DOCKER_PKG" ] || [ -z "$DOCKER_CROSSPLATFORMS" ]; then
	{
		echo "# WARNING! I don't seem to be running in the Docker container."
		echo "# The result of this command might be an incorrect build, and will not be"
		echo "#   officially supported."
		echo "#"
		echo "# Try this instead: make all"
		echo "#"
	} >&2
fi

echo

# List of bundles to create when no argument is passed
DEFAULT_BUNDLES=(
	validate-dco
	validate-gofmt

	binary

	test-unit
	test-integration
	test-integration-cli

	dynbinary
	dyntest-unit
	dyntest-integration

	cover
	cross
	tgz
	ubuntu
)

VERSION=$(cat ./VERSION) # 必须在顶层目录运行 hack/make.sh，因为VERSION在顶层

# 检索git命令是否存在
if command -v git &> /dev/null && git rev-parse &> /dev/null; then
  # 获取commitid
	GITCOMMIT=$(git rev-parse --short HEAD)
	# --procelain 精简输出
	# --untracked-files 忽略没有跟踪的文件
	# 意思就是所有被追踪的文件中有变更过，但是没有提交，即脏版本
	if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
		GITCOMMIT="$GITCOMMIT-dirty"
	fi
# 如果git命令不存在，但是提供了$DOCKER_GITCOMMIT变量，则做为提交号
elif [ "$DOCKER_GITCOMMIT" ]; then
	GITCOMMIT="$DOCKER_GITCOMMIT"
# 是在没办法锚定提交号就报错退出
else
	echo >&2 'error: .git directory missing and DOCKER_GITCOMMIT not specified'
	echo >&2 '  Please either build with the .git directory accessible, or specify the'
	echo >&2 '  exact (--short) commit hash you are building using DOCKER_GITCOMMIT for'
	echo >&2 '  future accountability in diagnosing build issues.  Thanks!'
	exit 1
fi

# 如果存在AOTU_GOPATH变量，清空，重构GOPATH
if [ "$AUTO_GOPATH" ]; then
	rm -rf .gopath
	mkdir -p .gopath/src/"$(dirname "${DOCKER_PKG}")"
	# GOPATH的特有，实际内容必须在PATH/src目录下
	ln -sf ../../../.. .gopath/src/"${DOCKER_PKG}"
	# ./../../.. 是在github.com父目录，如此能够构建一个临时的PATH
	export GOPATH="$(pwd)/.gopath:$(pwd)/vendor"
fi

# 如果GOPATH不存在则爆炸退出
if [ ! "$GOPATH" ]; then
	echo >&2 'error: missing GOPATH; please see http://golang.org/doc/code.html#GOPATH'
	echo >&2 '  alternatively, set AUTO_GOPATH=1'
	exit 1
fi

# 编译客户端
if [ -z "$DOCKER_CLIENTONLY" ]; then
	DOCKER_BUILDTAGS+=" daemon"
fi

# Use these flags when compiling the tests and final binary
# 连接桉树，提供了版本及对应git提交号
LDFLAGS='
	-w
	-X '$DOCKER_PKG'/dockerversion.GITCOMMIT "'$GITCOMMIT'"
	-X '$DOCKER_PKG'/dockerversion.VERSION "'$VERSION'"
'
LDFLAGS_STATIC='-linkmode external'
EXTLDFLAGS_STATIC='-static'
BUILDFLAGS=( -a -tags "netgo static_build $DOCKER_BUILDTAGS" )

# A few more flags that are specific just to building a completely-static binary (see hack/make/binary)
# PLEASE do not use these anywhere else.
EXTLDFLAGS_STATIC_DOCKER="$EXTLDFLAGS_STATIC -lpthread -Wl,--unresolved-symbols=ignore-in-object-files"
LDFLAGS_STATIC_DOCKER="
	$LDFLAGS_STATIC
	-X $DOCKER_PKG/dockerversion.IAMSTATIC true
	-extldflags \"$EXTLDFLAGS_STATIC_DOCKER\"
"

if [ "$(uname -s)" = 'FreeBSD' ]; then
	# Tell cgo the compiler is Clang, not GCC
	# https://code.google.com/p/go/source/browse/src/cmd/cgo/gcc.go?spec=svne77e74371f2340ee08622ce602e9f7b15f29d8d3&r=e6794866ebeba2bf8818b9261b54e2eef1c9e588#752
	export CC=clang

	# "-extld clang" is a workaround for
	# https://code.google.com/p/go/issues/detail?id=6845
	LDFLAGS="$LDFLAGS -extld clang"
fi

# If sqlite3.h doesn't exist under /usr/include,
# check /usr/local/include also just in case
# (e.g. FreeBSD Ports installs it under the directory)
if [ ! -e /usr/include/sqlite3.h ] && [ -e /usr/local/include/sqlite3.h ]; then
	export CGO_CFLAGS='-I/usr/local/include'
	export CGO_LDFLAGS='-L/usr/local/lib'
fi

HAVE_GO_TEST_COVER=
if \
	go help testflag | grep -- -cover > /dev/null \
	&& go tool -n cover > /dev/null 2>&1 \
; then
	HAVE_GO_TEST_COVER=1
fi

# If $TESTFLAGS is set in the environment, it is passed as extra arguments to 'go test'.
# You can use this to select certain tests to run, eg.
#
#   TESTFLAGS='-run ^TestBuild$' ./hack/make.sh test
#
go_test_dir() {
	dir=$1
	coverpkg=$2
	testcover=()
	if [ "$HAVE_GO_TEST_COVER" ]; then
		# if our current go install has -cover, we want to use it :)
		mkdir -p "$DEST/coverprofiles"
		coverprofile="docker${dir#.}"
		coverprofile="$DEST/coverprofiles/${coverprofile//\//-}"
		testcover=( -cover -coverprofile "$coverprofile" $coverpkg )
	fi
	(
		echo '+ go test' $TESTFLAGS "${DOCKER_PKG}${dir#.}"
		cd "$dir"
		go test ${testcover[@]} -ldflags "$LDFLAGS" "${BUILDFLAGS[@]}" $TESTFLAGS
	)
}

# Compile phase run by parallel in test-unit. No support for coverpkg
go_compile_test_dir() {
	dir=$1
	out_file="$DEST/precompiled/$dir.test"
	testcover=()
	if [ "$HAVE_GO_TEST_COVER" ]; then
		# if our current go install has -cover, we want to use it :)
		mkdir -p "$DEST/coverprofiles"
		coverprofile="docker${dir#.}"
		coverprofile="$DEST/coverprofiles/${coverprofile//\//-}"
		testcover=( -cover -coverprofile "$coverprofile" ) # missing $coverpkg
	fi
	if [ "$BUILDFLAGS_FILE" ]; then
		readarray -t BUILDFLAGS < "$BUILDFLAGS_FILE"
	fi
	(
		cd "$dir"
		go test "${testcover[@]}" -ldflags "$LDFLAGS" "${BUILDFLAGS[@]}" $TESTFLAGS -c
	)
	[ $? -ne 0 ] && return 1
	mkdir -p "$(dirname "$out_file")"
	mv "$dir/$(basename "$dir").test" "$out_file"
	echo "Precompiled: ${DOCKER_PKG}${dir#.}"
}

# This helper function walks the current directory looking for directories
# holding certain files ($1 parameter), and prints their paths on standard
# output, one per line.
find_dirs() {
	find . -not \( \
		\( \
			-wholename './vendor' \
			-o -wholename './integration' \
			-o -wholename './integration-cli' \
			-o -wholename './contrib' \
			-o -wholename './pkg/mflag/example' \
			-o -wholename './.git' \
			-o -wholename './bundles' \
			-o -wholename './docs' \
			-o -wholename './pkg/libcontainer/nsinit' \
		\) \
		-prune \
	\) -name "$1" -print0 | xargs -0n1 dirname | sort -u
}

hash_files() {
	while [ $# -gt 0 ]; do
		f="$1"
		shift
		dir="$(dirname "$f")"
		base="$(basename "$f")"
		for hashAlgo in md5 sha256; do
			if command -v "${hashAlgo}sum" &> /dev/null; then
				(
					# subshell and cd so that we get output files like:
					#   $HASH docker-$VERSION
					# instead of:
					#   $HASH /go/src/github.com/.../$VERSION/binary/docker-$VERSION
					cd "$dir"
					"${hashAlgo}sum" "$base" > "$base.$hashAlgo"
				)
			fi
		done
	done
}

bundle() {
	bundlescript=$1
	bundle=$(basename $bundlescript)
	# Making bundle: binary (in bundles/1.2.0/binary)
	echo "---> Making bundle: $bundle (in bundles/$VERSION/$bundle)"
	mkdir -p bundles/$VERSION/$bundle
	# source /go/src/github.com/docker/docker/hack/make/binary /go/src/github.com/docker/docker/bundles/1.2.0/binary
	# 跳转到另一个脚本，并附带参数
	source $bundlescript $(pwd)/bundles/$VERSION/$bundle
}

main() {
	# We want this to fail if the bundles already exist and cannot be removed.
	# This is to avoid mixing bundles from different versions of the code.
	mkdir -p bundles
	# bundles/1.2.0 目录如果存在说明编译过，删除目录重新编译
	if [ -e "bundles/$VERSION" ]; then
		echo "bundles/$VERSION already exists. Removing."
		rm -fr bundles/$VERSION && mkdir bundles/$VERSION || exit 1
		echo
	fi
	SCRIPTDIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
	if [ $# -lt 1 ]; then
		bundles=(${DEFAULT_BUNDLES[@]})
	else
		bundles=($@)
	fi
	for bundle in ${bundles[@]}; do
	  # bundle /go/src/github.com/docker/docker/hack/make/binary
	  # 执行子命令，位于 hack/make 每个命令一个独立脚本。类似于路由
		bundle $SCRIPTDIR/make/$bundle
		echo
	done
}

main "$@"

# hack/make.sh binary     输出在bundle/1.2.0/binary
# hack/make.sh dynbinary  输出在bundle/1.2.0/dynbinary