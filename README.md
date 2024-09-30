# ArgoCD Templating Configuration Management Plugin (Experimental)

## Overview

The ArgoCD Templating configuration management plugin allows for templating
(uses Helm under the hood) in manifests that ArgoCD deploys. Once the plugin is
installed, any ArgoCD `Application` can execute templates using the
`argocd-templating` service account in the application's destination namespace
specified in its `spec.destination.namespace`.

For example, the following `Application` will be able to resolve templates
leveraging the `argocd-templating` service account in the `my-app` namespace. It
is the cluster administrator's responsibility to create the `argocd-templating`
service account and grant it permissions. The service account must have `list`
and `watch` (`get` is not used) on any resource referenced by templates.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: my-app
  namespace: my-app
spec:
  project: my-app
  source:
    plugin:
      name: argocd-templating-v1.0
    repoURL: https://github.com/mprahl/argocd-testing.git
    targetRevision: argocd-templating-plugin
    path: "."
  destination:
    server: https://kubernetes.default.svc
    namespace: my-app
```

An example of a template would be something like the following, which applies
the label `i-am-from: template` to all namespaces in the cluster:

```yaml
{{- range $ns := (lookup "v1" "Namespace" "" "").items }}
---
apiVersion: v1
kind: Namespace
metadata:
  name: {{ $ns.metadata.name }}
  labels:
    i-am-from: template
{{- end }}
```

## Limitations

The following user experience limitations are known of this architecture:

- All templates are resolved on the cluster the ArgoCD instance is installed on,
  not the destination cluster.
- Based on sidecar plugin architecture, this plugin requires granting token
  creation access to the service account of the ArgoCD repo server. This is used
  to generate a token for the `argocd-templating` service accounts in the ArgoCD
  `Application` destination namespaces.
- It requires a hard refresh for the templates to reexecute. This is done
  automatically whenever the referenced template object is updated.

The following are some reason why this is considered experimental code:

- The code does not have test coverage.
- Token refresh error handling is not handled.
- The code doesn't handle multiple live ArgoCD repo servers. So if there is a
  rolling update, a manual hard refresh is required to "register" the ArgoCD
  `Application` with the plugin.
- `start.sh` doesn't seem to exit if the plugin crashes.
- State of no longer used service accounts aren't fully cleaned up until a
  restart of the container.

## Installing the Plugin

The following instructions assume the OpenShift GitOps operator is installed,
though this should work with the community ArgoCD operator as long as the
namespaces referenced are changed.

By applying the configuration below, it grants token creation access to the
default service account in the `openshift-gitops` namespace, installs the ArgoCD
`argocd-templating-v1.0` plugin leveraging the prebuilt
`quay.io/mprahl/argocd-templating-plugin:latest` container image, and configures
ArgoCD to allow applications in the `my-app` namespace (used in the example
below). You may choose to build the `Dockerfile` instead of using the prebuilt
image that may get out of date.

```bash
kubectl apply -f example-config/config.yaml
```

## Example Usage of the Plugin

### Lazy Way

Create the prerequisites with:

```bash
kubectl apply -f example-config/setup.yaml
```

### Guided Way

First create the `my-app` namespace as mentioned before:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: my-app
```

Then create a secret in the `default` namespace to be referenced in a template:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-secret
  namespace: default
  labels:
    hello: world3
stringData:
  city: Raleigh
  state: NC
```

Then create the `argocd-templating` service account in the `my-app` namespace
which has permission to get the `default/my-secret` `Secret` and list all
`Namespace` objects. This is the service account that will be used to execute
the templates of the ArgoCD `Application`:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: argocd-templating
  namespace: my-app
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: argocd-templating
  namespace: default
rules:
  - apiGroups:
      - ""
    resources:
      - "secrets"
    verbs:
      - list
      - watch
    resourceNames:
      - my-secret
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: argocd-templating
rules:
  - apiGroups:
      - ""
    resources:
      - "namespaces"
    verbs:
      - list
      - watch
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: argocd-templating
  namespace: default
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: argocd-templating
subjects:
  - kind: ServiceAccount
    name: argocd-templating
    namespace: my-app
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: argocd-templating
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: argocd-templating
subjects:
  - kind: ServiceAccount
    name: argocd-templating
    namespace: my-app
```

Finally, create the ArgoCD `AppProject` and `Application` to deploy the
[mprahl/argocd-testing](https://github.com/mprahl/argocd-testing/tree/ocm-templating-plugin)
repo on the `ocm-template-plugin` branch:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: my-app
  namespace: openshift-gitops
  # Finalizer that ensures that project is not deleted until it is not referenced by any application
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  description: My App
  sourceRepos:
    - "*"
  sourceNamespaces:
    - "*"
  destinations:
    - server: https://kubernetes.default.svc
      namespace: my-app
    - server: https://kubernetes.default.svc
      namespace: default
    - server: https://kubernetes.default.svc
  clusterResourceWhitelist:
    - group: ""
      kind: Namespace
  namespaceResourceWhitelist:
    - group: ""
      kind: ConfigMap
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: my-app
  namespace: my-app
spec:
  project: my-app
  source:
    plugin:
      name: argocd-templating-v1.0
    repoURL: https://github.com/mprahl/argocd-testing.git
    targetRevision: ocm-templating-plugin
    path: "."
  destination:
    server: https://kubernetes.default.svc
    namespace: my-app
```

The ArgoCD user interface should now show all the namespaces with the `kube-`
prefix on the cluster as out of sync due to a missing label, a missing namespace
of `my-app-config`, and a missing `ConfigMap` of `default/myapp`.
