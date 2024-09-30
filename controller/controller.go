package controller

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

func (r *ArgoCDAppReconciler) SetupWithManager(
	mgr ctrl.Manager, rawSources ...source.TypedSource[reconcile.Request],
) error {
	argocdApplication := &unstructured.Unstructured{}
	argocdApplication.SetGroupVersionKind(schema.GroupVersionKind{
		Kind:    "Application",
		Group:   "argoproj.io",
		Version: "v1alpha1",
	})

	builder := ctrl.NewControllerManagedBy(mgr).
		Named("argocd-app").
		For(
			argocdApplication,
			// TODO: Use a more efficient predicate
			builder.WithPredicates(predicate.Funcs{
				CreateFunc: func(e event.CreateEvent) bool {
					// TODO: The initial list from the list watch to trigger a hard refresh on everything doesn't work
					// when doing a rolling restart since ArgoCD uses the old repo server.
					obj := e.Object.(*unstructured.Unstructured)

					// TODO: Add support for sources list
					plugin, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "plugin", "name")

					return strings.HasPrefix(plugin, "argocd-templating-v")
				},
				UpdateFunc: func(e event.UpdateEvent) bool {
					oldObj := e.ObjectOld.(*unstructured.Unstructured)
					newObj := e.ObjectNew.(*unstructured.Unstructured)

					// TODO: Add support for sources list
					oldPlugin, _, _ := unstructured.NestedString(oldObj.Object, "spec", "source", "plugin", "name")
					newPlugin, _, _ := unstructured.NestedString(newObj.Object, "spec", "source", "plugin", "name")

					if !strings.HasPrefix(oldPlugin, "argocd-templating-v") &&
						!strings.HasPrefix(newPlugin, "argocd-templating-v") {
						return false
					}

					return oldPlugin != newPlugin
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					return true
				},
			}),
		)

	for _, rawSource := range rawSources {
		if rawSource != nil {
			builder = builder.WatchesRawSource(rawSource)
		}
	}

	return builder.Complete(r)
}

var _ reconcile.Reconciler = &ArgoCDAppReconciler{}

type ArgoCDAppReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=argoproj.io,resources=applications,verbs=get;list;watch;update;patch

// Reconcile is responsible for evaluating and rescheduling ConfigurationPolicy evaluations.
func (r *ArgoCDAppReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	klog.Infof("Reconciling %s", request.NamespacedName)

	app := unstructured.Unstructured{}
	app.SetGroupVersionKind(schema.GroupVersionKind{
		Kind:    "Application",
		Group:   "argoproj.io",
		Version: "v1alpha1",
	})

	err := r.Get(ctx, request.NamespacedName, &app)
	if err != nil {
		if errors.IsNotFound(err) {
			// TODO: Handle clean up of resolvers
			klog.Infof("Application was deleted: %s", request.NamespacedName)

			return ctrl.Result{}, nil
		}

		klog.Errorf("Failed to get the ArgoCD Application %s: %v", request.NamespacedName, err)

		return ctrl.Result{}, err
	}

	// TODO: Add support for sources list
	plugin, _, _ := unstructured.NestedString(app.Object, "spec", "source", "plugin", "name")
	if !strings.HasPrefix(plugin, "argocd-templating-v") {
		klog.Infof(
			"Application (%s) doesn't use the argocd-templating plugin, but uses: %s", request.NamespacedName, plugin,
		)
		// TODO: Handle clean up of resolvers
		return ctrl.Result{}, nil
	}

	klog.Infof("Refreshing the ArgoCD Application %s", request.NamespacedName)

	annotations := app.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string, 1)
	}

	annotations["argocd.argoproj.io/refresh"] = "hard"

	app.SetAnnotations(annotations)

	err = r.Update(ctx, &app)
	if err != nil {
		klog.Errorf("Failed to refresh the ArgoCD Application %s: %v", request.NamespacedName, err)

		return ctrl.Result{}, err
	}

	klog.Infof("The ArgoCD Application %s was refreshed", request.NamespacedName)

	return ctrl.Result{}, nil
}
