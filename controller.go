package main

import (
	"context"
	"os"

	"github.com/hashicorp/terraform/plugin"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	api "sigs.k8s.io/controller-runtime/examples/crd/pkg"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

var (
	setupLog = ctrl.Log.WithName("setup")
	recLog   = ctrl.Log.WithName("reconciler")
)

type controller struct {
	client.Client
	scheme       *runtime.Scheme
	tfReconciler *TerraformReconciler
	gvk          *schema.GroupVersionKind
}

func (r *controller) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	log := recLog.WithValues("generic reconciler", req.NamespacedName)
	log.V(1).Info("reconciling runtimeobj")
	ctx := context.Background()

	_ = ctx

	// Bug: Unstructured has no kind/schema look at runtime.Unstructured.
	var resource unstructured.Unstructured
	err := r.Client.Get(ctx, req.NamespacedName, &resource)
	if err != nil {
		log.Error(err, "Failed getting resource", "req", req.NamespacedName)
	}
	gen := resource.GetGeneration()
	log.Info("*** Reconciler called", "namespace", resource.GetNamespace(), "kind", resource.GetKind(), "name", resource.GetName(), "generation", gen)

	// Note this mutate the resource state
	// Todo: Return resource to make this clear from method maybe?
	r.tfReconciler.Reconcile(&resource)

	err = r.Client.Update(ctx, &resource)
	if err != nil {
		log.Error(err, "Failed saving resource")
		panic("Error updating CRD instance (Add)") // TODO handle retries
	}

	return ctrl.Result{}, nil
}

func runtimeObjFromGVK(r schema.GroupVersionKind) runtime.Object {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r)
	return obj
}

func setupControllerRuntime(provider *plugin.GRPCProvider, resources []GroupVersionFull) {
	ctrl.SetLogger(zap.Logger(true))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// in a real controller, we'd create a new scheme for this
	err = api.AddToScheme(mgr.GetScheme())
	if err != nil {
		setupLog.Error(err, "unable to add scheme")
		os.Exit(1)
	}

	for _, gv := range resources {
		setupLog.Info("Enabling controller for resource", "kind", gv.GroupVersionKind.Kind)
		err = ctrl.NewControllerManagedBy(mgr).
			// Note: Generation Changed Predicate means controller only called when an update is made to spec
			// or other case causing generation to change
			For(runtimeObjFromGVK(gv.GroupVersionKind), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
			Complete(&controller{
				Client:       mgr.GetClient(),
				tfReconciler: NewTerraformReconciler(provider),
				scheme:       mgr.GetScheme(),
				gvk:          &gv.GroupVersionKind,
			})
		if err != nil {
			setupLog.Error(err, "unable to create controller", "kind", gv.GroupVersionKind.Kind)
			os.Exit(1)
		}
	}

	// Todo: Enable webhooks in future
	// err = ctrl.NewWebhookManagedBy(mgr).
	// 	For(&api.ChaosPod{}).
	// 	Complete()
	// if err != nil {
	// 	setupLog.Error(err, "unable to create webhook")
	// 	os.Exit(1)
	// }

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
