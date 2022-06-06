#!/bin/bash

set -x

#docker pull docker-dev:v1.2.0
docker run -d -it --privileged \
	-e BUILDFLAGS -e DOCKER_CLIENTONLY -e DOCKER_EXECDRIVER -e DOCKER_EXPERIMENTAL \
	-e DOCKER_GRAPHDRIVER -e DOCKER_STORAGE_OPTS -e DOCKER_USERLANDPROXY -e TESTDIRS -e TESTFLAGS -e TIMEOUT \
	--name docker1.2.0 \
	-v `pwd`:/go/src/github.com/docker/docker docker-dev:v1.2.0 /sbin/init

#-v `pwd`/bundles:/go/src/github.com/docker/docker/bundles docker-dev:v1.2.0 /sbin/init

