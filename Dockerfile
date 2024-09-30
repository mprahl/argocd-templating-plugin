# Stage 1: Use image builder to build the target binaries

FROM registry.ci.openshift.org/stolostron/builder:go1.22-linux AS builder

WORKDIR /opt/repo
COPY . .
RUN go build -o argocd-templating-server cmd/server/main.go
RUN go build -o argocd-templating-client cmd/client/main.go

# Stage 2: Copy the binaries from the image builder to the base image
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest

# install operator binary
COPY --from=builder /opt/repo/argocd-templating-server /bin/argocd-templating-server
COPY --from=builder /opt/repo/argocd-templating-client /bin/argocd-templating-client
COPY --from=builder /opt/repo/start.sh /bin/start.sh

ENTRYPOINT ["/bin/start.sh"]

USER 999
