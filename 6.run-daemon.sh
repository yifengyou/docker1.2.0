#!/bin/bash

WORKDIR=`pwd`

if [ "${WORKDIR}" != "/go/src/github.com/docker/docker" ];then
	echo "Fatal! can't run dameon in host!"
	exit 1
fi


if [ ! -f bundles/1.2.0/binary/docker-1.2.0 ];then
	echo "file bundles/1.2.0/binary/docker-1.2.0"
	exit 1
fi

./bundles/1.2.0/binary/docker-1.2.0 -D -d
