#!/bin/bash

set -x

if [ ! -f hack/make.sh ];then
	echo "hack/make.sh not found!"
	exit 1
fi

hack/make.sh dynbinary

find ./bundles
