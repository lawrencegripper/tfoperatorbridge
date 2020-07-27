#!/bin/bash
set -e

SECONDS=0

echo "Waiting for the bridge to be up"
CRDS=$(kubectl get crd --all-namespaces | wc -l)

while [ "$CRDS" -eq "0" ] || [ "$CRDS" != "$NEW_CRDS" ]; do 
    if (( $SECONDS > 180))
        then 
        echo "Terminating due to timeout; This operation has taken $SECONDS seconds"
        exit 1
    fi

    CRDS=$(kubectl get crd --all-namespaces | wc -l)
    echo "There are $CRDS crds"
    echo "Sleeping for 10 seconds"
    sleep 10
    NEW_CRDS=$(kubectl get crd --all-namespaces | wc -l)
    echo "There are $NEW_CRDS crds"
done
