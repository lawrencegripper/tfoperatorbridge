package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	openapi_spec "github.com/go-openapi/spec"
	"github.com/hashicorp/terraform/providers"
	"github.com/lawrencegripper/tfoperatorbridge/tfprovider"
	"k8s.io/apimachinery/pkg/api/errors"
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
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	setupLog = ctrl.Log.WithName("setup")
	recLog   = ctrl.Log.WithName("reconciler")
)

const (
	encryptionKeyEnvVar = "TF_STATE_ENCRYPTION_KEY"
)

type controller struct {
	client.Client
	scheme       *runtime.Scheme
	tfReconciler *TerraformReconciler
	gvk          *schema.GroupVersionKind
}

func (r *controller) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	log := recLog.WithValues("name", req.NamespacedName)
	log.V(1).Info("reconciling runtimeobj")
	ctx := context.Background()

	_ = ctx

	// Note: Create an unstructued type for the Client to use and set the Kind
	// so that it can GET/UPDATE/DELETE etc
	resource := &unstructured.Unstructured{}
	resource.SetGroupVersionKind(*r.gvk)

	err := r.Client.Get(ctx, req.NamespacedName, resource)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed getting resource")
		return ctrl.Result{}, fmt.Errorf("Failed getting resource %q %w", req.NamespacedName.String(), err)
	}
	log = log.WithValues("kind", resource.GetKind(), "gen", resource.GetGeneration())

	// Note this mutate the resource state
	// Todo: Return resource to make this clear from method maybe?
	result, err := r.tfReconciler.Reconcile(ctx, log, resource)
	if err != nil {
		log.Error(err, "Failed TF Reconciler on resource")
		return ctrl.Result{}, fmt.Errorf("Failed TF Reconciler on resource %q %w", req.NamespacedName.String(), err)
	}
	if result != nil {
		return *result, nil
	}

	// Detect drift by checking resource every x mins
	// Todo: Make requeue time configurable
	return ctrl.Result{RequeueAfter: time.Minute * 15}, nil
}

func runtimeObjFromGVK(r schema.GroupVersionKind) runtime.Object {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r)
	return obj
}

func setupControllerRuntime(provider *tfprovider.TerraformProvider, resources []GroupVersionFull, schemas []openapi_spec.Schema) {
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

	var opts []TerraformReconcilerOption
	if encryptionKey := os.Getenv(encryptionKeyEnvVar); encryptionKey != "" {
		opts = append(opts, WithAesEncryption(encryptionKey))
	}

	gvkToReconcilerMap := make(map[string]*TerraformReconciler)

	for i, gv := range resources {
		groupVersionKind := gv.GroupVersionKind
		setupLog.Info("Enabling controller for resource", "kind", gv.GroupVersionKind.Kind)
		client := mgr.GetClient()
		schema := schemas[i] // TODO: Assumes schema and resources have the same index, make more reboust
		reconciler := NewTerraformReconciler(provider, client, schema, opts...)
		err = ctrl.NewControllerManagedBy(mgr).
			// Note: Generation Changed Predicate means controller only called when an update is made to spec
			// or other case causing generation to change
			For(runtimeObjFromGVK(gv.GroupVersionKind), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
			Complete(&controller{
				Client:       client,
				tfReconciler: reconciler,
				scheme:       mgr.GetScheme(),
				gvk:          &groupVersionKind,
			})
		if err != nil {
			setupLog.Error(err, "unable to create controller", "kind", gv.GroupVersionKind.Kind)
			os.Exit(1)
		}
		gvkToReconcilerMap[gv.GroupVersionKind.String()] = reconciler
	}

	// Todo: Enable webhooks in future
	// err = ctrl.NewWebhookManagedBy(mgr).
	// 	For(&api.ChaosPod{}).
	// 	Complete()
	// if err != nil {
	// 	setupLog.Error(err, "unable to create webhook")
	// 	os.Exit(1)
	// }

	hookServer := mgr.GetWebhookServer()
	hookServer.CertDir = "./certs"
	hookServer.Register("/validate-tf-crd", &webhook.Admission{Handler: &tfCRDValidator{tfReconcilers: gvkToReconcilerMap}})

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// tfCRDValidator validates Pods
type tfCRDValidator struct {
	Client        client.Client
	tfReconcilers map[string]*TerraformReconciler
	decoder       *admission.Decoder //nolint
}

// podValidator admits a pod iff a specific annotation exists.
func (v *tfCRDValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	resource := unstructured.Unstructured{}
	err := resource.UnmarshalJSON(req.Object.Raw)
	if err != nil {
		panic(err)
	}

	r := v.tfReconcilers[resource.GetObjectKind().GroupVersionKind().String()]
	resourceName := "azurerm_" + strings.Replace(resource.GetKind(), "-", "_", -1)
	schema := r.provider.Plugin.GetSchema().ResourceTypes[resourceName]

	spec, gotSpec, err := unstructured.NestedMap(resource.Object, "spec")
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("Error retrieving CRD spec: %s", err))
	}
	if !gotSpec {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("Error - CRD spec not found"))
	}

	terraformConfig := r.createEmptyTerraformValueForBlock(schema.Block, "test1")
	terraformConfig, statusMessage, err := r.mapCRDSpecValuesToTerraformConfig(ctx, schema.Block, terraformConfig, spec)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("Error applying values from the CRD spec to Terraform config: %s", err))
	}
	if terraformConfig == nil {
		// unable to retrieve referenced values - retry later
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("Unable to apply spec: %s", statusMessage))
	}

	valResp := r.provider.Plugin.ValidateResourceTypeConfig(providers.ValidateResourceTypeConfigRequest{
		TypeName: resourceName,
		Config:   *terraformConfig,
	})

	if valResp.Diagnostics.HasErrors() {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("Invalid CRD configuration: %w", valResp.Diagnostics.Err()))
	}

	return admission.Allowed("")
}
