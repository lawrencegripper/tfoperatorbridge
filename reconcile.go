package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/providers"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TerraformReconciler is a reconciler that processes CRD changes uses the configured Terraform provider
type TerraformReconciler struct {
	provider *plugin.GRPCProvider
	client   client.Client
}

func NewTerraformReconciler(provider *plugin.GRPCProvider, client client.Client) *TerraformReconciler {
	return &TerraformReconciler{
		provider: provider,
		client:   client,
	}
}

func (r *TerraformReconciler) Reconcile(ctx context.Context, log logr.Logger, crd *unstructured.Unstructured) (*ctrl.Result, error) {

	log.Info("Reconcile starting")
	// Get the kinds terraform schema
	kind := crd.GetKind()
	resourceName := "azurerm_" + strings.Replace(kind, "-", "_", -1)
	schema := r.provider.GetSchema().ResourceTypes[resourceName]

	var configValue *cty.Value
	var deleting bool
	if deleting = r.isDeleting(crd); deleting {
		// Deleting, so set a NullVal for the config
		v := cty.NullVal(schema.Block.ImpliedType())
		configValue = &v
	} else {
		if err := r.ensureFinalizer(ctx, log, crd); err != nil {
			return reconcileLogError(log, fmt.Errorf("Error adding finalizer: %s", err))
		}

		// Get the spec from the CRD
		spec, gotSpec, err := unstructured.NestedMap(crd.Object, "spec")
		if err != nil {
			return reconcileLogError(log, fmt.Errorf("Error retrieving CRD spec: %s", err))
		}
		if !gotSpec {
			return reconcileLogError(log, fmt.Errorf("Error - CRD spec not found"))
		}
		jsonSpecRaw, err := json.Marshal(spec)
		if err != nil {
			return reconcileLogError(log, fmt.Errorf("Error marshalling the CRD spec to JSON: %s", err))
		}

		// Create a TF cty.Value from the Spec JSON
		configValue = r.createEmptyResourceValue(schema, "test1")
		configValue, err = r.applySpecValuesToTerraformConfig(schema, configValue, string(jsonSpecRaw))
		if err != nil {
			return reconcileLogError(log, fmt.Errorf("Error applying values from the CRD spec to Terraform config: %s", err))
		}
	}

	state, err := r.getTerraformStateValue(crd, schema)
	if err != nil {
		return reconcileLogError(log, fmt.Errorf("Error getting Terraform state from CRD: %s", err))
	}
	log.Info("Terraform plan and apply...")
	newState, err := r.planAndApplyConfig(resourceName, *configValue, state)
	if err != nil {
		return reconcileLogError(log, fmt.Errorf("Error applying changes in Terraform: %s", err))
	}

	// Save the updated state early and as a separate operation
	// If this state is lost then the object needs to be imported, so avoid that as far as possible
	if err := r.saveTerraformStateValue(ctx, crd, newState); err != nil {
		// Also, return a ctrl.Result to avoid repeated retries
		return reconcileLogErrorWithResult(log, &ctrl.Result{}, fmt.Errorf("Error saving Terraform state to CRD: %s", err))
	}

	crdPreStateChanges := crd.DeepCopy()
	if err = r.setLastAppliedGeneration(crd); err != nil {
		return reconcileLogError(log, fmt.Errorf("Error saving last generation applied: %s", err))
	}

	if deleting {
		if err = r.removeFinalizerAndSave(ctx, log, crd); err != nil {
			return reconcileLogError(log, fmt.Errorf("Error removing finalizer: %s", err))
		}
	} else {
		if err = r.applyTerraformValueToCrdStatus(newState, crd); err != nil {
			return reconcileLogError(log, fmt.Errorf("Error mapping Terraform value to CRD status: %s", err))
		}
		if err = r.saveResourceStatus(ctx, crdPreStateChanges, crd); err != nil {
			return reconcileLogError(log, fmt.Errorf("Error saving CRD: %s", err))
		}
	}
	log.Info("Reconcile completed")
	return &ctrl.Result{}, nil
}
func reconcileLogError(log logr.Logger, err error) (*ctrl.Result, error) {
	return reconcileLogErrorWithResult(log, nil, err)
}
func reconcileLogErrorWithResult(log logr.Logger, result *ctrl.Result, err error) (*ctrl.Result, error) {
	log.Error(err, "Reconcile failed")
	return result, err
}

func (r *TerraformReconciler) isDeleting(resource *unstructured.Unstructured) bool {
	return resource.GetDeletionTimestamp() != nil
}
func (r *TerraformReconciler) ensureFinalizer(ctx context.Context, log logr.Logger, resource *unstructured.Unstructured) error {
	copyResource := resource.DeepCopy()

	finalizers := resource.GetFinalizers()
	for _, f := range finalizers {
		if f == "tfoperatorbridge.local" {
			// already present
			log.Info("Finalizer - already exists")
			return nil
		}
	}
	log.Info("Finalizer - adding")
	finalizers = append(finalizers, "tfoperatorbridge.local")
	resource.SetFinalizers(finalizers)

	if err := r.client.Patch(ctx, resource, client.MergeFrom(copyResource)); err != nil {
		return fmt.Errorf("Error saving finalizer: %s", err)
	}
	return nil
}
func (r *TerraformReconciler) removeFinalizerAndSave(ctx context.Context, log logr.Logger, resource *unstructured.Unstructured) error {
	copyResource := resource.DeepCopy()

	finalizers := resource.GetFinalizers()
	foundFinalizer := false
	updatedFinalizers := []string{}
	for _, f := range finalizers {
		if f == "tfoperatorbridge.local" {
			log.Info("Finalizer - removing")
			foundFinalizer = true
		} else {
			updatedFinalizers = append(updatedFinalizers, f)
		}
	}
	if !foundFinalizer {
		log.Info("Finalizer - not found on remove")
		return fmt.Errorf("Finalizer not found on remove")
	}
	resource.SetFinalizers(updatedFinalizers)
	if err := r.client.Patch(ctx, resource, client.MergeFrom(copyResource)); err != nil {
		return fmt.Errorf("Error removing finalizer: %s", err)
	}
	return nil
}
func (r *TerraformReconciler) getTerraformStateValue(resource *unstructured.Unstructured, schema providers.Schema) (*cty.Value, error) {
	tfStateString, gotTfState, err := unstructured.NestedString(resource.Object, "status", "_tfoperator", "tfState")
	if err != nil {
		return nil, err
	}
	if gotTfState {
		unmashaledState, err := ctyjson.Unmarshal([]byte(tfStateString), schema.Block.ImpliedType())
		if err != nil {
			return nil, err
		}
		return &unmashaledState, nil
	}
	emptyValue := schema.Block.EmptyValue()
	return &emptyValue, nil
}
func (r *TerraformReconciler) saveTerraformStateValue(ctx context.Context, resource *unstructured.Unstructured, state *cty.Value) error {
	copyResource := resource.DeepCopy()
	serializedState, err := ctyjson.Marshal(*state, state.Type())
	if err != nil {
		return fmt.Errorf("Error marshalling state: %s", err)
	}
	err = unstructured.SetNestedField(resource.Object, string(serializedState), "status", "_tfoperator", "tfState")
	if err != nil {
		return fmt.Errorf("Error setting tfState property: %s", err)
	}

	if err := r.client.Status().Patch(ctx, resource, client.MergeFrom(copyResource)); err != nil {
		return fmt.Errorf("Error saving tfState: %s", err)
	}
	return nil
}
func (r *TerraformReconciler) setLastAppliedGeneration(resource *unstructured.Unstructured) error {
	gen := resource.GetGeneration()
	err := unstructured.SetNestedField(resource.Object, strconv.FormatInt(gen, 10), "status", "_tfoperator", "lastAppliedGeneration")
	if err != nil {
		return fmt.Errorf("Error setting lastAppliedGeneration property: %s", err)
	}
	return nil
}
func (r *TerraformReconciler) createEmptyResourceValue(schema providers.Schema, resourceName string) *cty.Value {
	emptyValue := schema.Block.EmptyValue()
	valueMap := emptyValue.AsValueMap()
	valueMap["display_name"] = cty.StringVal(resourceName)
	value := cty.ObjectVal(valueMap)
	return &value
}

func (r *TerraformReconciler) applySpecValuesToTerraformConfig(schema providers.Schema, originalValue *cty.Value, jsonString string) (*cty.Value, error) {
	valueMap := originalValue.AsValueMap()

	mappedNames := map[string]bool{}
	var jsonData map[string]interface{}
	if err := json.Unmarshal([]byte(jsonString), &jsonData); err != nil {
		return nil, fmt.Errorf("Error unmarshalling JSON data: %s", err)
	}

	for name, attribute := range schema.Block.Attributes {
		jsonVal, gotJsonVal := jsonData[name]
		if gotJsonVal {
			v, err := r.getTerraformValueFromInterface(attribute.Type, jsonVal)
			if err != nil {
				return nil, fmt.Errorf("Error getting value for %q: %s", name, err)
			}
			valueMap[name] = *v
			mappedNames[name] = true
		}
	}
	unmappedNames := []string{}
	for jsonName := range jsonData {
		if !mappedNames[jsonName] {
			unmappedNames = append(unmappedNames, jsonName)
		}
	}

	// TODO handle schema.Block.BlockTypes as well as schema.Block.Attributes

	if len(unmappedNames) > 0 {
		return nil, fmt.Errorf("Unmapped values in JSON: %s", strings.Join(unmappedNames, ","))
	}

	newValue := cty.ObjectVal(valueMap)
	return &newValue, nil
}

func (r *TerraformReconciler) getTerraformValueFromInterface(t cty.Type, value interface{}) (*cty.Value, error) {
	// TODO handle other types: bool, int, float, list, ....
	if t.Equals(cty.String) {
		sv, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("Invalid value '%q' - expected 'string'", value)
		}
		val := cty.StringVal(sv)
		return &val, nil
	} else if t.Equals(cty.Bool) {
		bv, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("Invalid value '%q' - expected 'bool'", value)
		}
		val := cty.BoolVal(bv)
		return &val, nil
	} else if t.IsMapType() {
		elementType := t.MapElementType()
		mv, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("Invalid value '%q' - expected 'map[string]interface{}'", value)
		}
		resultMap := map[string]cty.Value{}
		for k, v := range mv {
			mapValue, err := r.getTerraformValueFromInterface(*elementType, v)
			if err != nil {
				return nil, fmt.Errorf("Error getting map value for property %q: %v", k, v)
			}
			resultMap[k] = *mapValue
		}
		result := cty.MapVal(resultMap)
		return &result, nil
	} else {
		return nil, fmt.Errorf("Unhandled type: %v", t.GoString())
	}
}

func (r *TerraformReconciler) applyTerraformValueToCrdStatus(value *cty.Value, crd *unstructured.Unstructured) error {
	valueMap := value.AsValueMap()

	status, gotStatus, err := unstructured.NestedMap(crd.Object, "status")
	if err != nil {
		return fmt.Errorf("Error getting status field: %s", err)
	}
	if !gotStatus {
		status = map[string]interface{}{}
	}
	// TODO  - drive this from the CRD status schema
	status["id"] = valueMap["id"].AsString()

	err = unstructured.SetNestedMap(crd.Object, status, "status")
	if err != nil {
		return fmt.Errorf("Error setting status field: %s", err)
	}

	return nil
}

func (r *TerraformReconciler) planAndApplyConfig(resourceName string, config cty.Value, state *cty.Value) (*cty.Value, error) {
	planResponse := r.provider.PlanResourceChange(providers.PlanResourceChangeRequest{
		TypeName:         resourceName,
		PriorState:       *state, // State after last apply or empty if non-existent
		ProposedNewState: config, // Config from CRD representing desired state
		Config:           config, // Config from CRD representing desired state ? Unsure why duplicated but hey ho.
	})

	if err := planResponse.Diagnostics.Err(); err != nil {
		log.Println(err.Error())
		return nil, fmt.Errorf("Failed in Terraform Plan: %s", err)
	}

	applyResponse := r.provider.ApplyResourceChange(providers.ApplyResourceChangeRequest{
		TypeName:     resourceName,              // Working theory:
		PriorState:   *state,                    // This is the state from the .tfstate file before the apply is made
		Config:       config,                    // The current HCL configuration or what would be in your terraform file
		PlannedState: planResponse.PlannedState, // The result of a plan (read / diff) between HCL Config and actual resource state
	})
	if err := applyResponse.Diagnostics.Err(); err != nil {
		log.Println(err.Error())
		return nil, fmt.Errorf("Failed in Terraform Apply: %s", err)
	}

	return &applyResponse.NewState, nil
}

func (r *TerraformReconciler) saveResourceStatus(ctx context.Context, originalResource *unstructured.Unstructured, resource *unstructured.Unstructured) error {

	err := r.client.Status().Patch(ctx, resource, client.MergeFrom(originalResource))
	if err != nil {
		//log.Error(err, "Failed saving resource")
		return fmt.Errorf("Failed saving resource %q %q %w", resource.GetNamespace(), resource.GetName(), err)
	}
	return nil
}
