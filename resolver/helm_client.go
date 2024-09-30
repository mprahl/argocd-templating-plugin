package resolver

import (
	"context"
	"errors"

	depclient "github.com/stolostron/kubernetes-dependency-watches/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
)

type helmProvider struct {
	dynamicWatcher depclient.DynamicWatcher
	watcher        depclient.ObjectIdentifier
}

func (h *helmProvider) GetClientFor(apiVersion, kind string) (dynamic.NamespaceableResourceInterface, bool, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, false, err
	}

	gvk := schema.GroupVersionKind{Group: gv.Group, Version: gv.Version, Kind: kind}

	scopedGVR, err := h.dynamicWatcher.GVKToGVR(gvk)
	if err != nil {
		return nil, false, err
	}

	return &dynamicWatcherResourceInterface{
		dynamicWatcher: h.dynamicWatcher, watcher: h.watcher, gvk: gvk,
	}, scopedGVR.Namespaced, nil
}

type dynamicWatcherResourceInterface struct {
	watcher        depclient.ObjectIdentifier
	namespace      string
	dynamicWatcher depclient.DynamicWatcher
	gvk            schema.GroupVersionKind
}

func (d *dynamicWatcherResourceInterface) Namespace(namespace string) dynamic.ResourceInterface {
	return &dynamicWatcherResourceInterface{
		namespace:      namespace,
		dynamicWatcher: d.dynamicWatcher,
		gvk:            d.gvk,
		watcher:        d.watcher,
	}
}

func (d *dynamicWatcherResourceInterface) Get(
	ctx context.Context, name string, _ metav1.GetOptions, _ ...string,
) (*unstructured.Unstructured, error) {
	// TODO: Verify no get options are passed
	return d.dynamicWatcher.Get(d.watcher, d.gvk, d.namespace, name)
}

func (d *dynamicWatcherResourceInterface) List(
	ctx context.Context, opts metav1.ListOptions,
) (*unstructured.UnstructuredList, error) {
	selector, err := labels.Parse(opts.LabelSelector)
	if err != nil {
		return nil, err
	}

	valuesList, err := d.dynamicWatcher.List(d.watcher, d.gvk, d.namespace, selector)
	if err != nil {
		return nil, err
	}

	return &unstructured.UnstructuredList{Items: valuesList}, nil
}

// Below are function stubs to support the client interface used by Helm, which is the dynamic.ResourceInterface which
// requires significantly more methods than are used by Helm.
func (d *dynamicWatcherResourceInterface) Create(
	_ context.Context, _ *unstructured.Unstructured, _ metav1.CreateOptions, _ ...string,
) (*unstructured.Unstructured, error) {
	return nil, errors.New("Create not supported")
}

func (d *dynamicWatcherResourceInterface) Update(
	_ context.Context, _ *unstructured.Unstructured, _ metav1.UpdateOptions, _ ...string,
) (*unstructured.Unstructured, error) {
	return nil, errors.New("Update not supported")
}

func (d *dynamicWatcherResourceInterface) UpdateStatus(
	_ context.Context, _ *unstructured.Unstructured, _ metav1.UpdateOptions,
) (*unstructured.Unstructured, error) {
	return nil, errors.New("UpdateStatus not supported")
}

func (d *dynamicWatcherResourceInterface) Delete(
	_ context.Context, _ string, _ metav1.DeleteOptions, _ ...string,
) error {
	return errors.New("Delete not supported")
}

func (d *dynamicWatcherResourceInterface) DeleteCollection(
	_ context.Context, _ metav1.DeleteOptions, _ metav1.ListOptions,
) error {
	return errors.New("Creates not supported")
}

func (d *dynamicWatcherResourceInterface) Watch(_ context.Context, _ metav1.ListOptions) (watch.Interface, error) {
	return nil, errors.New("Watch not supported")
}

func (d *dynamicWatcherResourceInterface) Patch(
	_ context.Context, _ string, _ types.PatchType, _ []byte, _ metav1.PatchOptions, _ ...string,
) (*unstructured.Unstructured, error) {
	return nil, errors.New("Patch not supported")
}

func (d *dynamicWatcherResourceInterface) Apply(
	_ context.Context, _ string, _ *unstructured.Unstructured, _ metav1.ApplyOptions, _ ...string,
) (*unstructured.Unstructured, error) {
	return nil, errors.New("Apply not supported")
}

func (d *dynamicWatcherResourceInterface) ApplyStatus(
	_ context.Context, _ string, _ *unstructured.Unstructured, _ metav1.ApplyOptions,
) (*unstructured.Unstructured, error) {
	return nil, errors.New("ApplyStatus not supported")
}
