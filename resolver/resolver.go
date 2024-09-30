package resolver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	depclient "github.com/stolostron/kubernetes-dependency-watches/client"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	authv1 "k8s.io/api/authentication/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/mprahl/argocd-templating-plugin/templaterequest"
)

var ErrSAMissing = errors.New("the serviceAccountName does not exist")

// TODO: Add clean up code when the resolver is no longer needed
type resolver struct {
	dynamicWatcher depclient.DynamicWatcher
	cancel         context.CancelFunc
}

type TemplateResolvers struct {
	templaterequest.UnimplementedTemplateResolverServer
	serverCtx                context.Context
	globalLock               sync.RWMutex
	DynamicWatcher           depclient.DynamicWatcher
	resolvers                map[types.NamespacedName]*resolver
	wg                       *sync.WaitGroup
	config                   *rest.Config
	appUpdates               chan event.GenericEvent
	dynamicWatcherReconciler *depclient.ControllerRuntimeSourceReconciler
	tokenRequestingClient    *kubernetes.Clientset
}

func NewTemplateResolvers(
	ctx context.Context,
	kubeconfig *rest.Config,
	tokenRequestingClient *kubernetes.Clientset,
	appUpdates chan event.GenericEvent,
) (*TemplateResolvers, source.TypedSource[reconcile.Request]) {
	dynamicWatcherReconciler, dynamicWatcherSource := depclient.NewControllerRuntimeSource()

	return &TemplateResolvers{
		serverCtx:                ctx,
		config:                   kubeconfig,
		dynamicWatcherReconciler: dynamicWatcherReconciler,
		tokenRequestingClient:    tokenRequestingClient,
		appUpdates:               appUpdates,
		resolvers:                map[types.NamespacedName]*resolver{},
		wg:                       &sync.WaitGroup{},
	}, dynamicWatcherSource
}

func (t *TemplateResolvers) Resolve(
	ctx context.Context, request *templaterequest.TemplateRequest,
) (*templaterequest.TemplateResponse, error) {
	if request.ArgoCDAppNamespace == "" {
		return nil, errors.New("the namespace was not provided")
	}

	if request.ArgoCDAppName == "" {
		return nil, errors.New("the name was not provided")
	}

	if request.ArgoCDAppDestination == "" {
		return nil, errors.New("the destination was not provided")
	}

	if request.LocalPath == "" {
		return nil, errors.New("the local path was not provided")
	}

	templates := []*chart.File{}

	yamlFiles, err := getYAMLFiles(request.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("failed to find the YAML files: %w", err)
	}

	for _, yamlFile := range yamlFiles {
		content, err := os.ReadFile(yamlFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read the YAML files: %w", err)
		}

		templates = append(templates, &chart.File{Name: yamlFile, Data: content})
	}

	saNSName := types.NamespacedName{
		Namespace: request.ArgoCDAppDestination,
		Name:      "argocd-templating",
	}

	t.globalLock.RLock()
	nsResolver := t.resolvers[saNSName]
	t.globalLock.RUnlock()

	if nsResolver == nil {
		t.globalLock.Lock()

		nsResolver = t.resolvers[saNSName]
		if nsResolver == nil {
			newResolver, err := t.initResolver(t.serverCtx, saNSName)
			if err != nil {
				t.globalLock.Unlock()

				return nil, fmt.Errorf("failed to initialize the resolver client: %v", err)
			}

			t.resolvers[saNSName] = newResolver
			nsResolver = newResolver

			t.globalLock.Unlock()
		}
	}

	// TODO: Add locking around this application to take care of paralel requests
	watcher := depclient.ObjectIdentifier{
		Group:     "argoproj.io",
		Version:   "v1alpha1",
		Kind:      "Application",
		Namespace: request.ArgoCDAppNamespace,
		Name:      request.ArgoCDAppName,
	}

	err = nsResolver.dynamicWatcher.StartQueryBatch(watcher)
	if err != nil {
		return nil, err
	}

	defer nsResolver.dynamicWatcher.EndQueryBatch(watcher)

	provider := &helmProvider{
		watcher:        watcher,
		dynamicWatcher: nsResolver.dynamicWatcher,
	}

	templateChart := chart.Chart{
		Metadata: &chart.Metadata{
			APIVersion: "v2",
			Name:       "argocd-template-plugin",
			Version:    "1.0.0",
			Type:       "application",
		},
		Templates: templates,
	}

	vals, err := engine.RenderWithClientProvider(&templateChart, chartutil.Values{}, provider)
	if err != nil {
		return nil, fmt.Errorf("failed to render the templates: %v", err)
	}

	resolvedYAML := ""

	for _, val := range vals {
		val := strings.TrimSpace(val)

		if val == "" {
			continue
		}

		if resolvedYAML != "" {
			if strings.HasPrefix(val, "---\n") {
				resolvedYAML += "\n"
			} else {
				resolvedYAML += "\n---\n"
			}
		}

		resolvedYAML += val
	}

	return &templaterequest.TemplateResponse{ResolvedYAML: resolvedYAML}, nil
}

type TokenRefreshConfig struct {
	// The token lifetime in seconds.
	ExpirationSeconds int64
	// The minimum refresh minutes before expiration. This must be <= MaxRefreshMins.
	MinRefreshMins float64
	// The maximum refresh minutes before expiration. This must be >= MinRefreshMins.
	MaxRefreshMins float64
	// If a token refresh encountered an unrecoverable error, then this is called.
	OnFailedRefresh func(error)
}

func (t *TemplateResolvers) initResolver(
	ctx context.Context,
	serviceAccount types.NamespacedName,
) (*resolver, error) {
	resolverCtx, resolverCtxCancel := context.WithCancel(ctx)

	// Refresh the token anywhere between 1h50m and 1h55m after issuance.
	tokenConfig := TokenRefreshConfig{
		ExpirationSeconds: 7200, // 2 hours,
		MinRefreshMins:    5,
		MaxRefreshMins:    10,
		// TODO: Add code to retrigger the templates on failure
		OnFailedRefresh: func(originalErr error) { panic(originalErr) },
	}

	// Use BearerTokenFile since it allows the token to be renewed without restarting the template resolver.
	tokenFile, err := GetToken(resolverCtx, t.wg, t.tokenRequestingClient, serviceAccount, tokenConfig)
	if err != nil {
		// Make this error identifiable so that up in the call stack, a watch can be add to the service account
		// to trigger a reconcile when the service account becomes available.
		if k8serrors.IsNotFound(err) {
			err = errors.Join(ErrSAMissing, err)
		}

		resolverCtxCancel()

		return nil, err
	}

	saConfig := getUserKubeConfig(t.config, tokenFile)

	dynamicWatcher, err := depclient.New(
		saConfig,
		t.dynamicWatcherReconciler,
		&depclient.Options{
			DisableInitialReconcile: true,
			EnableCache:             true,
		},
	)
	if err != nil {
		resolverCtxCancel()

		return nil, fmt.Errorf(
			"failed to instantiate the template resolver for the service account %s: %w", serviceAccount, err,
		)
	}

	rv := resolver{
		cancel: resolverCtxCancel,
	}

	t.wg.Add(1)

	go func() {
		err := dynamicWatcher.Start(resolverCtx)
		if err != nil {
			// TODO: Better error handling
			panic(err)
		}

		resolverCtxCancel()
		t.wg.Done()
	}()

	<-dynamicWatcher.Started()

	rv.dynamicWatcher = dynamicWatcher

	return &rv, nil
}

// GetToken will use the TokenRequest API to get a token for the service account and return a file path to where the
// token is stored. A new token will be requested and stored in the file before the token expires. If an unrecoverable
// error occurs during a token refresh, refreshConfig.OnFailedRefresh is called if it's defined.
func GetToken(
	ctx context.Context,
	wg *sync.WaitGroup,
	client *kubernetes.Clientset,
	serviceAccount types.NamespacedName,
	refreshConfig TokenRefreshConfig,
) (string, error) {
	namespace := serviceAccount.Namespace
	saName := serviceAccount.Name
	tokenReq := &authv1.TokenRequest{
		Spec: authv1.TokenRequestSpec{
			ExpirationSeconds: &refreshConfig.ExpirationSeconds,
		},
	}

	tokenReq, err := client.CoreV1().ServiceAccounts(namespace).CreateToken(
		ctx, saName, tokenReq, metav1.CreateOptions{},
	)
	if err != nil {
		return "", err
	}

	tokenFile, err := os.CreateTemp("", fmt.Sprintf("token-%s.%s-*", namespace, saName))
	if err != nil {
		return "", err
	}

	tokenFilePath, err := filepath.Abs(tokenFile.Name())
	if err != nil {
		return "", err
	}

	var writeErr error

	defer func() {
		if writeErr != nil {
			if removeErr := os.Remove(tokenFilePath); removeErr != nil {
				klog.Errorf("Failed to clean up the service account token file at %s: %v", tokenFilePath, removeErr)
			}
		}
	}()

	if _, writeErr = tokenFile.Write([]byte(tokenReq.Status.Token)); writeErr != nil {
		klog.Errorf("Failed to write the service account token file at %s: %v", tokenFilePath, err)

		return "", writeErr
	}

	if writeErr = tokenFile.Close(); writeErr != nil {
		klog.Errorf("Failed to close the service account token file at %s: %v", tokenFilePath, writeErr)

		if removeErr := os.Remove(tokenFilePath); removeErr != nil {
			klog.Errorf("Failed to clean up the service account token file at %s: %v", tokenFilePath, removeErr)
		}

		return "", err
	}

	expirationTimestamp := tokenReq.Status.ExpirationTimestamp

	wg.Add(1)

	go func() {
		klog.V(2).Infof(
			"Got a token that expires at %s for %s/%s",
			expirationTimestamp.UTC().Format(time.RFC3339), namespace, saName,
		)

		defer func() {
			if err := os.Remove(tokenFilePath); err != nil {
				klog.Errorf("Failed to clean up the service account token file at %s: %v", tokenFilePath, err)
			}

			wg.Done()
		}()

		// The latest refresh of the token is MinRefreshMins minutes before expiration
		minRefreshBuffer := time.Duration(refreshConfig.MinRefreshMins * float64(time.Minute))
		// Get the acceptable range of minutes that can be variable for the jitter below
		maxJitterMins := (refreshConfig.MaxRefreshMins - refreshConfig.MinRefreshMins) * float64(time.Minute)

		for {
			// Add a jitter between 0 and maxJitterMins to make the token renewals variable
			jitter := time.Duration(rand.Float64() * maxJitterMins).Round(time.Second) // #nosec G404
			// Refresh the token between 5 and 10 minutes from now
			refreshTime := expirationTimestamp.Add(-minRefreshBuffer).Add(-jitter)

			klog.V(2).Infof("Waiting to refresh the token for %s/%s at %s", namespace, saName, refreshTime)

			deadlineCtx, deadlineCtxCancel := context.WithDeadline(ctx, refreshTime)

			<-deadlineCtx.Done()

			deadlineErr := deadlineCtx.Err()

			// This really does nothing but this satisfies the linter that cancel is called.
			deadlineCtxCancel()

			if !errors.Is(deadlineErr, context.DeadlineExceeded) {
				return
			}

			klog.V(1).Infof("Refreshing the token for %s/%s", namespace, saName)

			tokenReq := &authv1.TokenRequest{
				Spec: authv1.TokenRequestSpec{
					ExpirationSeconds: &refreshConfig.ExpirationSeconds,
				},
			}

			tokenReq, err = client.CoreV1().ServiceAccounts(namespace).CreateToken(
				ctx, saName, tokenReq, metav1.CreateOptions{},
			)
			if err != nil {
				klog.Errorf(
					"Failed to renew the service account token for %s/%s; stopping the template resolver: %v",
					namespace, saName, err,
				)

				if refreshConfig.OnFailedRefresh != nil {
					refreshConfig.OnFailedRefresh(err)
				}

				return
			}

			err = os.WriteFile(tokenFilePath, []byte(tokenReq.Status.Token), 0o600)
			if err != nil {
				klog.Errorf(
					"The refreshed token couldn't be written to for %s/%s; stopping the template resolver: %v",
					namespace, saName, err,
				)

				if refreshConfig.OnFailedRefresh != nil {
					refreshConfig.OnFailedRefresh(err)
				}

				return
			}

			expirationTimestamp = tokenReq.Status.ExpirationTimestamp
		}
	}()

	return tokenFilePath, nil
}

// getUserKubeConfig will copy the URL and TLS related configuration from `config` and set the input token file when
// generating a new configuration. No authentication configuration is copied over.
func getUserKubeConfig(config *rest.Config, tokenFile string) *rest.Config {
	userConfig := &rest.Config{
		Host:    config.Host,
		APIPath: config.APIPath,
		TLSClientConfig: rest.TLSClientConfig{
			CAFile:     config.TLSClientConfig.CAFile,
			CAData:     config.TLSClientConfig.CAData,
			ServerName: config.TLSClientConfig.ServerName,
			Insecure:   config.TLSClientConfig.Insecure,
		},
	}

	userConfig.BearerTokenFile = tokenFile

	return userConfig
}

func getYAMLFiles(startingPath string) ([]string, error) {
	yamlFiles := []string{}

	err := filepath.WalkDir(startingPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		if d.IsDir() {
			return nil
		}

		fileExt := filepath.Ext(d.Name())
		if fileExt == ".yaml" || fileExt == ".yml" {
			yamlFiles = append(yamlFiles, path)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return yamlFiles, nil
}
