#!/bin/bash
# TODO: When one crashes, the pod doesn't stop
/bin/argocd-templating-server &
/var/run/argocd/argocd-cmp-server $@ &
wait -n

exit $?
