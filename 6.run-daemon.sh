#!/bin/bash


if [ ! -f bundles/1.2.0/binary/docker-1.2.0 ];then
	echo "file bundles/1.2.0/binary/docker-1.2.0"
	exit 1
fi

./bundles/1.2.0/binary/docker-1.2.0 -D -d
