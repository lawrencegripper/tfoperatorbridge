#!/bin/bash
set -e

CLUSTER_NAME="tob"
CLUSTER_EXISTS=$(kind get clusters | grep -q -o "^$CLUSTER_NAME\$" ; echo $?)
if [[ $CLUSTER_EXISTS == "0" ]]; then
    echo "Cluster '$CLUSTER_NAME' already exists"
else
    echo "Creating cluster '$CLUSTER_NAME' ..."
    kind create cluster --name tob
fi
kind export kubeconfig --name tob
