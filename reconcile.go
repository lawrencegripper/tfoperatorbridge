package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/providers"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TerraformReconcilerOption is modifying function to add functionality to the TerraformReconciler struct
type TerraformReconcilerOption func(*TerraformReconciler)

// WithAesEncryption is an TerraformReconcilerOption to add AES cipher to terraform state
func WithAesEncryption(encryptionKey string) TerraformReconcilerOption {
	return func(r *TerraformReconciler) {
		r.cipher = NewAesCipher([]byte(encryptionKey))
	}
}

// TerraformReconciler is a reconciler that processes CRD changes uses the configured Terraform provider
type TerraformReconciler struct {
	provider *plugin.GRPCProvider
	client   client.Client
	cipher   TerraformStateCipher
}

// NewTerraformReconciler creates a terraform reconciler
func NewTerraformReconciler(provider *plugin.GRPCProvider, client client.Client, opts ...TerraformReconcilerOption) *TerraformReconciler {
	r := &TerraformReconciler{
		provider: provider,
		client:   client,
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

type GetReferencedObjectValueResult struct {
	Value         *string // nil if not retrieved
	Error         error
	StatusMessage string // contains info message when Value is nil but there is not an error
}
type GetTerraformValueResult struct {
	Property      string
	Value         *cty.Value
	Error         error
	StatusMessage string
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
		var statusMessage string
		configValue, statusMessage, err = r.applySpecValuesToTerraformConfig(ctx, schema, configValue, string(jsonSpecRaw))
		if err != nil {
			return reconcileLogError(log, fmt.Errorf("Error applying values from the CRD spec to Terraform config: %s", err))
		}
		if configValue == nil {
			// unable to retrieve referenced values - retry later
			log.Info(fmt.Sprintf("Reconcile - requeing. Unable to apply spec: %s", statusMessage))
			return &ctrl.Result{RequeueAfter: time.Second * 30}, nil // TODO - configurable retry time?
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
		if err = r.setProvisioningState(crd, "Created"); err != nil { // TODO define the states!
			return reconcileLogError(log, fmt.Errorf("Error saving provisioning state applied: %s", err))
		}
		// TODO: Rename to mapTerraformValueToCrdStatus
		if err = r.applyTerraformValueToCrdStatus(schema, newState, crd); err != nil {
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
		var stateBytes []byte
		if r.cipher != nil {
			decodedState, err := base64.StdEncoding.DecodeString(tfStateString)
			if err != nil {
				return nil, fmt.Errorf("Error decoding terraform state: %+v", err)
			}
			decryptedState, err := r.cipher.Decrypt(string(decodedState))
			if err != nil {
				return nil, fmt.Errorf("Error decrypting terraform state: %+v", err)
			}
			stateBytes = []byte(decryptedState)
		} else {
			stateBytes = []byte(tfStateString)
		}

		unmashaledState, err := ctyjson.Unmarshal(stateBytes, schema.Block.ImpliedType())
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

	var stateBytes []byte
	if r.cipher != nil {
		encryptedState, err := r.cipher.Encrypt(string(serializedState))
		if err != nil {
			return err
		}
		encodedState := base64.StdEncoding.EncodeToString([]byte(encryptedState))
		stateBytes = []byte(encodedState)
	} else {
		stateBytes = serializedState
	}

	err = unstructured.SetNestedField(resource.Object, string(stateBytes), "status", "_tfoperator", "tfState")
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
func (r *TerraformReconciler) getProvisioningState(resource *unstructured.Unstructured) (string, error) {
	val, gotVal, err := unstructured.NestedString(resource.Object, "status", "_tfoperator", "provisioningState")
	if err != nil {
		return "", fmt.Errorf("Error setting provisioningState property: %s", err)
	}
	if !gotVal {
		val = ""
	}
	return val, nil
}
func (r *TerraformReconciler) setProvisioningState(resource *unstructured.Unstructured, state string) error {
	err := unstructured.SetNestedField(resource.Object, state, "status", "_tfoperator", "provisioningState")
	if err != nil {
		return fmt.Errorf("Error setting provisioningState property: %s", err)
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

// applySpecValuesToTerraformConfig
// returns
//  cty.Value - the applied value or nil if not successful
//  string    - a status message if we failed to apply without an error condition
//  error     - non-nil if an error occurred
func (r *TerraformReconciler) applySpecValuesToTerraformConfig(ctx context.Context, schema providers.Schema, originalValue *cty.Value, jsonString string) (*cty.Value, string, error) {
	valueMap := originalValue.AsValueMap()

	mappedNames := map[string]bool{}
	var jsonData map[string]interface{}
	if err := json.Unmarshal([]byte(jsonString), &jsonData); err != nil {
		return nil, "", fmt.Errorf("Error unmarshalling JSON data: %s", err)
	}

	for name, attribute := range schema.Block.Attributes {
		jsonVal, gotJsonVal := jsonData[name]
		if gotJsonVal {
			getTerraformValueResult := r.getTerraformValueFromInterface(ctx, attribute.Type, jsonVal)
			propName := name
			if getTerraformValueResult.Property != "" {
				propName = propName + "." + getTerraformValueResult.Property
			}
			if getTerraformValueResult.Error != nil {
				return nil, "", fmt.Errorf("Error getting value for %q: %s", propName, getTerraformValueResult.Error)
			}
			if getTerraformValueResult.Value == nil {
				return nil, fmt.Sprintf("Unabile to retrieve value for %q: %s", propName, getTerraformValueResult.StatusMessage), nil
			}
			valueMap[name] = *getTerraformValueResult.Value
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
		return nil, "", fmt.Errorf("Unmapped values in JSON: %s", strings.Join(unmappedNames, ","))
	}

	newValue := cty.ObjectVal(valueMap)
	return &newValue, "", nil
}

func (r *TerraformReconciler) getTerraformValueFromInterface(ctx context.Context, t cty.Type, value interface{}) GetTerraformValueResult {
	// TODO handle other types: bool, int, float, list, ....
	if t.Equals(cty.String) {
		sv, ok := value.(string)
		if !ok {
			return GetTerraformValueResult{Error: fmt.Errorf("Invalid value '%q' - expected 'string'", value)}
		}
		if strings.HasPrefix(sv, "`") { // TODO Consider making the prefix configurable
			referencedObjectValueResult := r.getReferencedObjectValue(ctx, sv)
			if referencedObjectValueResult.Error != nil {
				return GetTerraformValueResult{
					Error:         fmt.Errorf("Error looking up referenced value: %s", referencedObjectValueResult.Error),
					StatusMessage: referencedObjectValueResult.StatusMessage,
				}
			}
			if referencedObjectValueResult.Value == nil {
				// referenced value couldn't be retrieved - return nil error to retry later
				return GetTerraformValueResult{
					StatusMessage: referencedObjectValueResult.StatusMessage,
				}
			}
			sv = *referencedObjectValueResult.Value
		}
		val := cty.StringVal(sv)
		return GetTerraformValueResult{Value: &val}
	} else if t.Equals(cty.Bool) {
		bv, ok := value.(bool)
		if !ok {
			return GetTerraformValueResult{Error: fmt.Errorf("Invalid value '%q' - expected 'bool'", value)}
		}
		val := cty.BoolVal(bv)
		return GetTerraformValueResult{Value: &val}
	} else if t.IsMapType() {
		elementType := t.MapElementType()
		mv, ok := value.(map[string]interface{})
		if !ok {
			return GetTerraformValueResult{Error: fmt.Errorf("Invalid value '%q' - expected 'map[string]interface{}'", value)}
		}
		resultMap := map[string]cty.Value{}
		for k, v := range mv {
			getTerraformValueResult := r.getTerraformValueFromInterface(ctx, *elementType, v)
			propName := k
			if getTerraformValueResult.Property != "" {
				propName = propName + "." + getTerraformValueResult.Property
			}
			if getTerraformValueResult.Error != nil {
				return GetTerraformValueResult{
					Property: propName,
					Error:    fmt.Errorf("Error getting map value for property %q: %v", k, v),
				}
			}
			if getTerraformValueResult.Value == nil {
				return GetTerraformValueResult{
					Property:      propName,
					StatusMessage: getTerraformValueResult.StatusMessage,
				}
			}
			resultMap[k] = *getTerraformValueResult.Value
		}
		val := cty.MapVal(resultMap)
		return GetTerraformValueResult{Value: &val}
	} else {
		return GetTerraformValueResult{Error: fmt.Errorf("Unhandled type: %v", t.GoString())}
	}
}

func (r *TerraformReconciler) getReferencedObjectValue(ctx context.Context, referenceString string) GetReferencedObjectValueResult {
	// TODO - parse the reference
	// e.g. group:version:kind:namespace:name:propertypath

	if strings.HasPrefix(referenceString, "`") {
		referenceString = strings.TrimPrefix(referenceString, "`")
	} else {
		return GetReferencedObjectValueResult{
			Error: fmt.Errorf("input does not begin with the backtick escape character"),
		}
	}
	referenceString = strings.TrimSpace(referenceString)

	referenceParts := strings.Split(referenceString, ":")
	if len(referenceParts) != 6 {
		return GetReferencedObjectValueResult{
			Error: fmt.Errorf("input should be in the format group:version:kind:namespace:name:propertypath. Got %q", referenceString),
		}
	}

	group := referenceParts[0]
	version := referenceParts[1]
	kind := referenceParts[2]
	namespacedName := types.NamespacedName{
		Namespace: referenceParts[3],
		Name:      referenceParts[4],
	}
	propertyPath := referenceParts[5]

	resource := &unstructured.Unstructured{}
	resource.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   group,
		Version: version,
		Kind:    kind,
	})

	err := r.client.Get(ctx, namespacedName, resource)
	if err != nil {
		var statusError *k8sErrors.StatusError
		if errors.As(err, &statusError) {
			if statusError.ErrStatus.Code == 404 {
				// not found - might not have been created
				// return nil error as this should fall into the controlled retry
				return GetReferencedObjectValueResult{
					StatusMessage: fmt.Sprintf("Referenced object not found: '%s:%s:%s:%s:%s'", group, version, kind, namespacedName.Namespace, namespacedName.Name),
				}
			}
		}
		return GetReferencedObjectValueResult{
			Error: fmt.Errorf("Error getting referenced resource: %s", err),
		}
	}

	provisioningState, err := r.getProvisioningState(resource)
	if err != nil {
		return GetReferencedObjectValueResult{
			Error: fmt.Errorf("Error getting provisioningState for referenced resource : %s", err),
		}
	}
	if provisioningState != "Created" {
		return GetReferencedObjectValueResult{
			StatusMessage: fmt.Sprintf("Referenced object not provisined yet: '%s:%s:%s:%s:%s'", group, version, kind, namespacedName.Namespace, namespacedName.Name),
		}
	}

	propertyPathParts := strings.Split(propertyPath, ".")
	value, gotValue, err := unstructured.NestedString(resource.Object, propertyPathParts...)
	if err != nil {
		return GetReferencedObjectValueResult{
			Error: fmt.Errorf("Error retrieving property %q: %s", propertyPath, err),
		}
	}
	if gotValue {
		return GetReferencedObjectValueResult{
			Value: &value,
		}
	}
	return GetReferencedObjectValueResult{
		StatusMessage: fmt.Sprintf("Value of property not found %q", propertyPath),
	}
}

func (r *TerraformReconciler) applyTerraformValueToCrdStatus(schema providers.Schema, value *cty.Value, crd *unstructured.Unstructured) error {
	valueMap := value.AsValueMap()

	status, gotStatus, err := unstructured.NestedMap(crd.Object, "status")
	if err != nil {
		return fmt.Errorf("Error getting status field: %s", err)
	}
	if !gotStatus {
		status = map[string]interface{}{}
	}

	for k, v := range valueMap {
		value, err := r.getValueFromCtyValue(k, &v, schema.Block)
		if err != nil {
			return err
		}

		status[k] = value
	}

	err = unstructured.SetNestedMap(crd.Object, status, "status")
	if err != nil {
		return fmt.Errorf("Error setting status field: %s", err)
	}

	return nil
}

func (r *TerraformReconciler) getAttributeForCtyKey(key string, block *configschema.Block) (*configschema.Attribute, error) {
	if block == nil {
		return nil, fmt.Errorf("Cannot find attribute in nil block")
	}

	// Is the attribute in this block?
	for k, v := range block.Attributes {
		if k == key {
			// log.Printf("Key %s found", key) // TODO: uncomment when debug logging supported
			return v, nil
		}
	}

	// Is the attribute in a child block?
	for bKey, bVal := range block.BlockTypes {
		// Is the key a block?
		if bKey == key {
			// log.Printf("Key %s is a nested block", bKey)  // TODO: uncomment when debug logging supported
			return nil, nil // nil, nil indicates this key belongs to a block not an attribute
		}
		a, e := r.getAttributeForCtyKey(bKey, &bVal.Block)
		if a != nil {
			return a, nil // We found an attribute
		}
		if a == nil && e == nil {
			return nil, nil // We found a block
		}
		// An error here just indicates we couldn't find the attribute or child block in the current block - we should keep looking
	}

	return nil, fmt.Errorf("Unable to find attribute or block with the key %s", key)
}

func (r *TerraformReconciler) getValueFromCtyValue(key string, value *cty.Value, block *configschema.Block) (interface{}, error) {
	if value.IsNull() {
		// log.Printf("key %s has null value\n", key) // TODO: uncomment when debug logging supported
		return nil, nil
	}

	// Get the attribute for this cty key
	attr, err := r.getAttributeForCtyKey(key, block)
	if err != nil {
		log.Printf("Key not found in schema. Skipping %s\n", key)
		return nil, nil
	}
	var sensitive bool
	// Blocks are not attributes in the schema but still represent a value
	isBlock := (attr == nil)
	if isBlock {
		sensitive = false // Blocks can't be sensitive
	} else {
		sensitive = attr.Sensitive
	}

	ctyType := value.Type()
	if ctyType.Equals(cty.String) {
		s := value.AsString()
		if sensitive {
			if r.cipher != nil {
				log.Printf("Key %s is sensitive, encrypting value", key)
				s, err = r.cipher.Encrypt(s)
				if err != nil {
					return nil, err
				}
			}
		}
		return s, nil
	} else if ctyType.Equals(cty.Bool) {
		return value.True(), nil
	} else if ctyType.Equals(cty.Number) {
		return value.AsBigFloat().Float64, nil
	} else if ctyType.IsMapType() || ctyType.IsObjectType() {
		valueMap := value.AsValueMap()
		nestedMap := make(map[string]interface{})
		for k, v := range valueMap {
			nestedValue, err := r.getValueFromCtyValue(k, &v, block)
			if err != nil {
				return nil, err
			}
			nestedMap[k] = nestedValue
		}
		return nestedMap, nil
	} else if ctyType.IsListType() {
		valueList := value.AsValueSlice()
		list := make([]interface{}, len(valueList))
		for _, item := range valueList {
			value, err := r.getValueFromCtyValue(key, &item, block)
			if err != nil {
				return nil, err
			}
			list = append(list, value)
		}
		return list, nil
	} else if ctyType.IsSetType() {
		valueSet := value.AsValueSet().Values()
		list := make([]interface{}, len(valueSet))
		for _, item := range valueSet {
			value, err := r.getValueFromCtyValue(key, &item, block)
			if err != nil {
				return nil, err
			}
			list = append(list, value)
		}
		return list, nil
	}

	return nil, fmt.Errorf("Value type is unknown %+v. Skipping key %v", ctyType.GoString(), key)
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
