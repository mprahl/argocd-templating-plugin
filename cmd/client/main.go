package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/mprahl/argocd-templating-plugin/templaterequest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	workingDir, err := os.Getwd()
	if err != nil {
		log.Fatalf("failed to get the working directory: %v", err)
	}

	destinationNamespace := os.Getenv("ARGOCD_APP_NAMESPACE")
	if destinationNamespace == "" {
		log.Fatal("The ARGOCD_APP_NAMESPACE environment variable is not set")
	}

	nsName := os.Getenv("ARGOCD_APP_NAME")
	if nsName == "" {
		log.Fatal("The ARGOCD_APP_NAME environment variable is not set")
	}

	var namespace string
	var name string

	// ARGOCD_APP_NAME is in the format of <namespace>_<name>
	splitNsName := strings.SplitN(nsName, "_", 2)
	if len(splitNsName) != 2 {
		log.Fatalf("The ARGOCD_APP_NAME environment variable does not have a namespace set: %s", nsName)
	}

	namespace = splitNsName[0]
	name = splitNsName[1]

	grpcConnection, err := grpc.NewClient("localhost:8080", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("failed to connect to the gRPC server: %v", err)
	}

	defer grpcConnection.Close()

	ctx := context.TODO()
	req := &templaterequest.TemplateRequest{
		ArgoCDAppName:        name,
		ArgoCDAppNamespace:   namespace,
		ArgoCDAppDestination: destinationNamespace,
		LocalPath:            workingDir,
	}

	client := templaterequest.NewTemplateResolverClient(grpcConnection)

	response, err := client.Resolve(ctx, req)
	if err != nil {
		log.Fatalf("Failed to resolve templates: %v", err)
	}

	fmt.Println(response.ResolvedYAML)
}
