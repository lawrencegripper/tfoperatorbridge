package k8s

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
	openapi_spec "github.com/go-openapi/spec"
	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/providers"
	"github.com/lawrencegripper/tfoperatorbridge/pkg/tfprovider"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	crdStatusKeyName                       = "status"
	crdStatusTerraformOperatorKeyName      = "_tfoperator"
	crdStatusTerraformStateKeyName         = "tfState"
	crdStatusLastAppliedGenerationKeyName  = "lastAppliedGeneration"
	crdStatusProvisioningStateKeyName      = "provisioningState"
	crdStatusProviderNameKeyName           = "providerName"
	crdStatusProviderVersionKeyName        = "providerVersion"
	crdStatusProviderChecksumSHA256KeyName = "providerChecksumSHA256"
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
	provider      *tfprovider.TerraformProvider
	client        client.Client
	cipher        TerraformStateCipher
	openAPISchema openapi_spec.Schema
}

// NewTerraformReconciler creates a terraform reconciler
func NewTerraformReconciler(provider *tfprovider.TerraformProvider, client client.Client, schema openapi_spec.Schema, opts ...TerraformReconcilerOption) *TerraformReconciler {
	r := &TerraformReconciler{
		provider:      provider,
		client:        client,
		openAPISchema: schema,
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// GetReferencedObjectValueResult : Todo
type GetReferencedObjectValueResult struct {
	Value         *string // nil if not retrieved
	Error         error
	StatusMessage string // contains info message when Value is nil but there is not an error
}

// GetTerraformValueResult : Todo
type GetTerraformValueResult struct {
	Property      string
	Value         *cty.Value
	Error         error
	StatusMessage string
}

// Reconcile updates the current state of the resource to match the desired state in the CRD
func (r *TerraformReconciler) Reconcile(ctx context.Context, log logr.Logger, crd *unstructured.Unstructured) (*ctrl.Result, error) {
	log.Info("Reconcile starting")
	// Get the kinds terraform schema
	kind := crd.GetKind()
	resourceName := "azurerm_" + strings.Replace(kind, "-", "_", -1)
	schema := r.provider.Plugin.GetSchema().ResourceTypes[resourceName]

	var terraformConfig *cty.Value
	var deleting bool
	if deleting = r.isDeleting(crd); deleting {
		// Deleting, so set a NullVal for the config
		v := cty.NullVal(schema.Block.ImpliedType())
		terraformConfig = &v
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
			return reconcileLogError(log, fmt.Errorf("Error marshaling the CRD spec to JSON: %s", err))
		}

		// Create a TF cty.Value from the Spec JSON
		terraformConfig = r.createEmptyTerraformValueForBlock(schema.Block, "test1")
		var statusMessage string
		// Unmarshal CRD spec JSON string to a value map
		crdSpecValues := make(map[string]interface{})
		if err = json.Unmarshal([]byte(string(jsonSpecRaw)), &crdSpecValues); err != nil {
			return nil, fmt.Errorf("Error unmarshalling JSON data: %s", err)
		}
		terraformConfig, statusMessage, err = r.mapCRDSpecValuesToTerraformConfig(ctx, schema.Block, terraformConfig, crdSpecValues)
		if err != nil {
			return reconcileLogError(log, fmt.Errorf("Error applying values from the CRD spec to Terraform config: %s", err))
		}
		if terraformConfig == nil {
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
	newState, err := r.planAndApplyConfig(resourceName, *terraformConfig, state)
	if err != nil {
		return reconcileLogError(log, fmt.Errorf("Error applying changes in Terraform: %s", err))
	}

	// Save the updated state early and as a separate operation
	// If this state is lost then the object needs to be imported, so avoid that as far as possible
	if err = r.saveTerraformStateValue(ctx, crd, newState); err != nil {
		// Also, return a ctrl.Result to avoid repeated retries
		return reconcileLogErrorWithResult(log, &ctrl.Result{}, fmt.Errorf("Error saving Terraform state to CRD: %s", err))
	}

	crdPreStateChanges := crd.DeepCopy()
	if err = r.setLastAppliedGeneration(crd); err != nil {
		return reconcileLogError(log, fmt.Errorf("Error saving last generation applied: %s", err))
	}

	if err = r.ensureTerraformProviderMetadata(crd); err != nil {
		return reconcileLogError(log, fmt.Errorf("Error adding terraform provider metadata: %s", err))
	}

	if deleting {
		if err = r.removeFinalizerAndSave(ctx, log, crd); err != nil {
			return reconcileLogError(log, fmt.Errorf("Error removing finalizer: %s", err))
		}
	} else {
		if err = r.setProvisioningState(crd, "Created"); err != nil { // TODO define the states!
			return reconcileLogError(log, fmt.Errorf("Error saving provisioning state applied: %s", err))
		}
		if err = r.mapTerraformValuesToCRDStatus(schema, newState, crd); err != nil {
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
	tfStateString, gotTfState, err := unstructured.NestedString(resource.Object, crdStatusKeyName, crdStatusTerraformOperatorKeyName, crdStatusTerraformStateKeyName)
	if err != nil {
		return nil, err
	}
	if gotTfState {
		var stateString string
		var decodedAndDecrypted bool
		if stateString, decodedAndDecrypted, err = r.decodeAndDecryptString(tfStateString); err != nil {
			return nil, fmt.Errorf("couldn't decode and decrypt terraform state: %+v", err)
		}
		if !decodedAndDecrypted {
			// TODO: Handle failure to decrypt properly
			log.Println("Warning, did not decrypt state")
		}
		unmashaledState, err := ctyjson.Unmarshal([]byte(stateString), schema.Block.ImpliedType())
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
		return fmt.Errorf("Error marshaling state: %s", err)
	}
	var stateString string
	var encodedAndEncrypted bool
	if stateString, encodedAndEncrypted, err = r.encryptAndEncodeString(string(serializedState)); err != nil {
		return fmt.Errorf("couldn't encode and encrypt terraform state: %+v", err)
	}
	if !encodedAndEncrypted {
		// TODO: Handle failure to encrypt properly
		log.Println("Warning, did not encrypt state")
	}

	err = unstructured.SetNestedField(resource.Object, stateString, crdStatusKeyName, crdStatusTerraformOperatorKeyName, crdStatusTerraformStateKeyName)
	if err != nil {
		return fmt.Errorf("Error setting %s property: %s", crdStatusTerraformStateKeyName, err)
	}

	if err := r.client.Status().Patch(ctx, resource, client.MergeFrom(copyResource)); err != nil {
		return fmt.Errorf("Error saving %s: %s", crdStatusTerraformStateKeyName, err)
	}
	return nil
}

func (r *TerraformReconciler) ensureTerraformProviderMetadata(resource *unstructured.Unstructured) error {
	if err := r.ensureTerraformProviderMetadataValue(resource, crdStatusProviderNameKeyName, r.provider.Metadata.Name); err != nil {
		return err
	}
	if err := r.ensureTerraformProviderMetadataValue(resource, crdStatusProviderVersionKeyName, r.provider.Metadata.Version); err != nil {
		return err
	}
	if err := r.ensureTerraformProviderMetadataValue(resource, crdStatusProviderChecksumSHA256KeyName, r.provider.Metadata.ChecksumSHA256); err != nil {
		return err
	}
	return nil
}

func (r *TerraformReconciler) ensureTerraformProviderMetadataValue(resource *unstructured.Unstructured, key, value string) error {
	_, got, err := unstructured.NestedString(resource.Object, crdStatusKeyName, crdStatusTerraformOperatorKeyName, key)
	if err != nil {
		return fmt.Errorf("Error getting %s field: %s", key, err)
	}
	if !got {
		err := unstructured.SetNestedField(resource.Object, value, crdStatusKeyName, crdStatusTerraformOperatorKeyName, key)
		if err != nil {
			return fmt.Errorf("Error setting %s property: %s", key, err)
		}
	}
	return nil
}

func (r *TerraformReconciler) setLastAppliedGeneration(resource *unstructured.Unstructured) error {
	gen := resource.GetGeneration()
	err := unstructured.SetNestedField(resource.Object, strconv.FormatInt(gen, 10), crdStatusKeyName, crdStatusTerraformOperatorKeyName, crdStatusLastAppliedGenerationKeyName)
	if err != nil {
		return fmt.Errorf("Error setting %s property: %s", crdStatusLastAppliedGenerationKeyName, err)
	}
	return nil
}

func (r *TerraformReconciler) getProvisioningState(resource *unstructured.Unstructured) (string, error) {
	val, gotVal, err := unstructured.NestedString(resource.Object, crdStatusKeyName, crdStatusTerraformOperatorKeyName, crdStatusProvisioningStateKeyName)
	if err != nil {
		return "", fmt.Errorf("Error setting %s property: %s", crdStatusProvisioningStateKeyName, err)
	}
	if !gotVal {
		val = ""
	}
	return val, nil
}

func (r *TerraformReconciler) setProvisioningState(resource *unstructured.Unstructured, state string) error {
	err := unstructured.SetNestedField(resource.Object, state, crdStatusKeyName, crdStatusTerraformOperatorKeyName, crdStatusProvisioningStateKeyName)
	if err != nil {
		return fmt.Errorf("Error setting %s property: %s", crdStatusProvisioningStateKeyName, err)
	}
	return nil
}

func (r *TerraformReconciler) createEmptyTerraformValueForBlock(block *configschema.Block, resourceName string) *cty.Value {
	emptyValue := block.EmptyValue()
	valueMap := emptyValue.AsValueMap()
	valueMap["display_name"] = cty.StringVal(resourceName)
	value := cty.ObjectVal(valueMap)
	return &value
}

// mapCRDSpecValuesToTerraformConfig takes a maps values from a CRD spec to a terraform config value respecting the terraform block schema.
// returns:
// - cty.Value, updated terraform config value or nil if unsuccessful
// - string, 	a status message if mapping failed without an error condition
// - error, 	not nil if an error occurred during mapping
func (r *TerraformReconciler) mapCRDSpecValuesToTerraformConfig(ctx context.Context, terraformBlock *configschema.Block, terraformConfig *cty.Value, crdSpecValues map[string]interface{}) (*cty.Value, string, error) {
	terraformConfigValueMap := terraformConfig.AsValueMap()
	// For each attribute in this schema block
	for attrName, attr := range terraformBlock.Attributes {
		// Get the matching attribute name from the CRD map
		crdValue, foundAttributeInCRD := crdSpecValues[attrName]
		if foundAttributeInCRD {
			// If found, get the cty value from the CRD value
			getTerraformValueResult := r.getTerraformValueFromInterface(ctx, attr.Type, crdValue)
			propName := attrName
			if getTerraformValueResult.Property != "" {
				propName = propName + "." + getTerraformValueResult.Property
			}
			if getTerraformValueResult.Error != nil {
				return nil, "", fmt.Errorf("Error getting value for %q: %s", propName, getTerraformValueResult.Error)
			}
			if getTerraformValueResult.Value == nil {
				return nil, fmt.Sprintf("Unabile to retrieve value for %q: %s", propName, getTerraformValueResult.StatusMessage), nil
			}
			if getTerraformValueResult.Value.IsNull() {
				log.Printf("Skipping attribute %s as has a null value in CRD", attrName)
				continue // Skip attributes in schema that have a null value in the CRD
			}
			valType := getTerraformValueResult.Value.Type()
			if valType.IsCollectionType() {
				if getTerraformValueResult.Value.LengthInt() == 0 {
					log.Printf("Skipping collection attribute %s as it is empty in CRD", attrName)
					continue // Skip attributes in schema that have an empty value in the CRD
				}
			}
			// log.Printf("Adding terraform attribute %s with value %+v", attrName, getTerraformValueResult.Value) // TODO: uncomment when debug logging supported
			terraformConfigValueMap[attrName] = *getTerraformValueResult.Value
		}
	}

	// For each nested block in the terraform schema, get the CRD spec values and map to terraform values
	for nestedTerraformBlockName, nestedTerraformBlock := range terraformBlock.BlockTypes {
		crdBlockProperty, foundNestedTerraformBlockInCRD := crdSpecValues[nestedTerraformBlockName]
		// If the block was found in the CRD spec values
		if foundNestedTerraformBlockInCRD {
			if isTerraformNestedBlockAOpenAPIObjectProperty(nestedTerraformBlock) {
				// Nested terraform blocks that map to objects are directly assigned as properties
				crdBlockValueMap := crdBlockProperty.(map[string]interface{})
				terraformValue := r.createEmptyTerraformValueForBlock(&nestedTerraformBlock.Block, nestedTerraformBlockName)
				updatedTerraformValue, statusMessage, err := r.mapCRDSpecValuesToTerraformConfig(ctx, &nestedTerraformBlock.Block, terraformValue, crdBlockValueMap)
				if err != nil {
					return nil, "", err
				}
				if statusMessage != "" {
					return nil, statusMessage, nil
				}
				updatedTerraformValue, _ = unflattenTerraformCollectionValue(nestedTerraformBlockName, updatedTerraformValue, nestedTerraformBlock)
				// log.Printf("Adding terraform block %s with value %+v", nestedBlockName, updatedValue) // TODO: uncomment when debug logging supported
				terraformConfigValueMap[nestedTerraformBlockName] = *updatedTerraformValue
			} else {
				// Nested terraform blocks that map to arrays are wrapped in an array property before being assign to a property
				var updatedValuesSlice []cty.Value
				nestedCRDBlockArray := crdBlockProperty.([]interface{})
				// Traverse each instance of the block in the array
				for _, nestedCRDBlockItem := range nestedCRDBlockArray {
					// Map it to a terraform type and add it to a collection
					nestedCRDValues := nestedCRDBlockItem.(map[string]interface{})
					nestedValue := r.createEmptyTerraformValueForBlock(&nestedTerraformBlock.Block, nestedTerraformBlockName)
					updatedValue, statusMessage, err := r.mapCRDSpecValuesToTerraformConfig(ctx, &nestedTerraformBlock.Block, nestedValue, nestedCRDValues)
					if err != nil {
						return nil, "", err
					}
					if statusMessage != "" {
						return nil, statusMessage, nil
					}
					updatedValuesSlice = append(updatedValuesSlice, *updatedValue)
				}
				// log.Printf("Adding terraform block %s with value %+v", nestedBlockName, updatedValue) // TODO: uncomment when debug logging supported
				if nestedTerraformBlock.Nesting == configschema.NestingList {
					terraformConfigValueMap[nestedTerraformBlockName] = cty.ListVal(updatedValuesSlice)
				} else if nestedTerraformBlock.Nesting == configschema.NestingSet {
					terraformConfigValueMap[nestedTerraformBlockName] = cty.SetVal(updatedValuesSlice)
				}
			}

		}
	}

	newValue := cty.ObjectVal(terraformConfigValueMap)
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
	} else if t.IsListType() {
		elementType := t.ListElementType()
		lv, ok := value.([]interface{})
		if !ok {
			return GetTerraformValueResult{Error: fmt.Errorf("Invalid value '%q' - expected '[]interface{}'", value)}
		}
		resultList := []cty.Value{}
		for _, v := range lv {
			getTerraformValueResult := r.getTerraformValueFromInterface(ctx, *elementType, v)
			var propName string
			if getTerraformValueResult.Property != "" {
				propName = getTerraformValueResult.Property
			}
			if getTerraformValueResult.Error != nil {
				return GetTerraformValueResult{
					Property: propName,
					Error:    fmt.Errorf("Error getting list value for property %q: %v", propName, v),
				}
			}
			if getTerraformValueResult.Value == nil {
				return GetTerraformValueResult{
					Property:      propName,
					StatusMessage: getTerraformValueResult.StatusMessage,
				}
			}
			resultList = append(resultList, *getTerraformValueResult.Value)
		}
		val := cty.ListVal(resultList)
		return GetTerraformValueResult{Value: &val}
	} else if t.IsSetType() {
		elementType := t.SetElementType()
		lv, ok := value.([]interface{})
		if !ok {
			return GetTerraformValueResult{Error: fmt.Errorf("Invalid value '%q' - expected '[]interface{}'", value)}
		}
		resultSet := []cty.Value{}
		for _, v := range lv {
			getTerraformValueResult := r.getTerraformValueFromInterface(ctx, *elementType, v)
			var propName string
			if getTerraformValueResult.Property != "" {
				propName = getTerraformValueResult.Property
			}
			if getTerraformValueResult.Error != nil {
				return GetTerraformValueResult{
					Property: propName,
					Error:    fmt.Errorf("Error getting set value for property %q: %v", propName, v),
				}
			}
			if getTerraformValueResult.Value == nil {
				return GetTerraformValueResult{
					Property:      propName,
					StatusMessage: getTerraformValueResult.StatusMessage,
				}
			}
			resultSet = append(resultSet, *getTerraformValueResult.Value)
		}
		val := cty.SetVal(resultSet)
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

func walkOpenAPISchemaProperties(schema *openapi_spec.Schema, fn func(string, *openapi_spec.Schema) error) error {
	for propName, propValue := range schema.Properties {
		err := fn(propName, &propValue)
		if err != nil {
			return err
		}

		err = walkOpenAPISchemaProperties(&propValue, fn)
		if err != nil {
			return err
		}
	}
	return nil
}

func getTerraformAttributeOrNestedBlockFromBlock(key string, block *configschema.Block) (*configschema.Attribute, *configschema.NestedBlock) {
	for attrName, attrVal := range block.Attributes {
		if key == attrName {
			return attrVal, nil
		}
	}
	for nestedBlockName, nestedBlockVal := range block.BlockTypes {
		if key == nestedBlockName {
			return nil, nestedBlockVal
		}
		attr, block := getTerraformAttributeOrNestedBlockFromBlock(key, &nestedBlockVal.Block)
		if attr != nil {
			return attr, nil
		}
		if block != nil {
			return nil, block
		}
	}
	return nil, nil
}

func (r *TerraformReconciler) encryptAndEncodeString(plain string) (string, bool, error) {
	if r.cipher != nil {
		encrypted, err := r.cipher.Encrypt(plain)
		if err != nil {
			return plain, false, err
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(encrypted))
		return encoded, true, nil
	}
	return plain, false, nil
}

func (r *TerraformReconciler) decodeAndDecryptString(encoded string) (string, bool, error) {
	if r.cipher != nil {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return encoded, false, err
		}
		decrypted, err := r.cipher.Decrypt(string(decoded))
		if err != nil {
			return encoded, false, err
		}
		return decrypted, true, nil
	}
	return encoded, false, nil
}

// getOpenAPIValueFromTerraformValue gets a value respecting the openapi schema from a terraform value
func (r *TerraformReconciler) getOpenAPIValueFromTerraformValue(terraformKey string, terraformValue *cty.Value, terraformAttr *configschema.Attribute, terraformBlock *configschema.NestedBlock, openAPIProperty *openapi_spec.Schema) (interface{}, error) {
	if terraformValue == nil {
		return nil, fmt.Errorf("Cannot get openapi value from nil terraform value")
	}

	var sensitive bool
	var collectionsRequireFlattening bool

	isTerraformBlock := (terraformAttr == nil)
	if isTerraformBlock {
		// Blocks can't be sensitive
		sensitive = false
		// Some terraform blocks represent value that should be flattened to an object in the openapi schema
		collectionsRequireFlattening = isTerraformNestedBlockAOpenAPIObjectProperty(terraformBlock)
	} else {
		sensitive = terraformAttr.Sensitive
	}

	ty := terraformValue.Type()
	if ty.Equals(cty.String) {
		if !openAPIProperty.Type.Contains("string") {
			return nil, fmt.Errorf("Cannot map key %s, only able to map from terraform type string to openapi type string, not type %+v", terraformKey, openAPIProperty.Type)
		}
		if terraformValue.IsNull() {
			return "", nil
		}
		var err error
		s := terraformValue.AsString()
		if sensitive {
			var b64AndEncrypted bool
			if s, b64AndEncrypted, err = r.encryptAndEncodeString(s); err != nil {
				return nil, err
			}
			if !b64AndEncrypted {
				// TODO: Handle failure to encrypt properly
				log.Printf("Warning, did not encrypt sensitve value %s", terraformKey)
			}
		}
		return s, nil
	}
	if ty.Equals(cty.Number) {
		if !openAPIProperty.Type.Contains("number") {
			return nil, fmt.Errorf("Cannot map key %s, only able to map from terraform type number to openapi type number, not type %+v", terraformKey, openAPIProperty.Type)
		}
		if terraformValue.IsNull() {
			return 0, nil
		}
		bf := terraformValue.AsBigFloat()
		if openAPIProperty.Format == "float" {
			f32, _ := bf.Float32()
			return f32, nil
		} else if openAPIProperty.Format == "double" {
			f64, _ := bf.Float64()
			return f64, nil
		} else {
			return nil, fmt.Errorf("Unsupported number format %s", openAPIProperty.Format)
		}
	}
	if ty.Equals(cty.Bool) {
		if !openAPIProperty.Type.Contains("boolean") && !openAPIProperty.Type.Contains("string") {
			return nil, fmt.Errorf("Cannot map key %s, only able to map from terraform type bool to openapi type [boolean, string], not type %+v", terraformKey, openAPIProperty.Type)
		}
		if terraformValue.IsNull() {
			return false, nil
		}
		return terraformValue.True(), nil
	}
	if ty.IsListType() && !collectionsRequireFlattening {
		if !openAPIProperty.Type.Contains("array") {
			return nil, fmt.Errorf("Cannot map key %s, only able to map from terraform type list to openapi type array, not type %+v", terraformKey, openAPIProperty.Type)
		}
		if terraformValue.IsNull() {
			var zeroArr []interface{}
			return zeroArr, nil
		}
		var arr []interface{}
		valueSlice := terraformValue.AsValueSlice()
		for _, val := range valueSlice {
			v, err := r.getOpenAPIValueFromTerraformValue(terraformKey, &val, terraformAttr, nil, openAPIProperty.Items.Schema)
			if err != nil {
				return nil, err
			}
			if v == nil {
				continue // skip nil values
			}
			arr = append(arr, v)
		}
		return arr, nil
	}
	if ty.IsSetType() && !collectionsRequireFlattening {
		if !openAPIProperty.Type.Contains("array") {
			return nil, fmt.Errorf("Cannot map key %s, only able to map from terraform type set to openapi type array, not set %+v", terraformKey, openAPIProperty.Type)
		}
		if terraformValue.IsNull() {
			var zeroArr []interface{}
			return zeroArr, nil
		}
		var arr []interface{}
		valueSlice := terraformValue.AsValueSlice()
		for _, val := range valueSlice {
			v, err := r.getOpenAPIValueFromTerraformValue(terraformKey, &val, terraformAttr, nil, openAPIProperty.Items.Schema)
			if err != nil {
				return nil, err
			}
			if v == nil {
				continue // skip nil values
			}
			arr = append(arr, v)
		}
		return arr, nil
	}
	// For objects and maps, first flatten if required
	terraformValue, flattened := flattenTerraformCollectionValue(terraformValue, terraformBlock)
	if ty.IsMapType() || ty.IsObjectType() || flattened {
		if !openAPIProperty.Type.Contains("object") {
			return nil, fmt.Errorf("Cannot map key %s, only able to map from terraform type map/object to openapi type object, not set %+v", terraformKey, openAPIProperty.Type)
		}
		if terraformValue.IsNull() {
			if ty.IsMapType() {
				zeroMap := map[string]interface{}{}
				return zeroMap, nil
			}
			if ty.IsObjectType() {
				var zeroObj interface{}
				return zeroObj, nil
			}
			return nil, fmt.Errorf("Cannot determine zero value for unsupported type %+v", ty)
		}
		vm := make(map[string]interface{})
		valueMap := terraformValue.AsValueMap()
		for k, v := range valueMap {
			var terraformNestedBlock *configschema.Block
			if terraformBlock != nil {
				terraformNestedBlock = &terraformBlock.Block
			}
			val, err := r.getCRDValueFromTerraformValue(k, &v, terraformNestedBlock)
			if err != nil {
				return nil, err
			}
			if val == nil {
				continue // skip nil values
			}
			vm[k] = val
		}
		return vm, nil
	}
	return nil, fmt.Errorf("Unable to map terraform key %s with value %+v and type %+v to a open api value", terraformKey, terraformValue, ty)
}

// getCRDValueFromTerraformValue derives a CRD value from a given terraform value
func (r *TerraformReconciler) getCRDValueFromTerraformValue(key string, value *cty.Value, block *configschema.Block) (interface{}, error) {
	var result interface{}
	// TODO: Optimize!

	// Walk the OpenAPI schema and...
	err := walkOpenAPISchemaProperties(&r.openAPISchema, func(name string, prop *openapi_spec.Schema) error {
		// Find the matching openapi property...
		if name != key {
			return nil // continue looking...
		}
		// Find the matching terraform attribute...
		terraformAttr, terraformBlock := getTerraformAttributeOrNestedBlockFromBlock(key, block)
		if terraformAttr == nil && terraformBlock == nil {
			return fmt.Errorf("Couldn't find terraform attribute with the name %s", key)
		}
		// Get the terraform value as an openapi value
		val, err := r.getOpenAPIValueFromTerraformValue(key, value, terraformAttr, terraformBlock, prop)
		if err != nil {
			return err
		}

		// Capture the resulting value
		result = val
		return nil
	})
	return result, err
}

// mapTerraformValuesToCRDStatus maps terraform values into an unstructed CRD structure that respects the CRD's OpenAPI status schema
func (r *TerraformReconciler) mapTerraformValuesToCRDStatus(schema providers.Schema, value *cty.Value, crd *unstructured.Unstructured) error {
	status, gotStatus, err := unstructured.NestedMap(crd.Object, crdStatusKeyName)
	if err != nil {
		return fmt.Errorf("Error getting status field: %s", err)
	}
	if !gotStatus {
		status = map[string]interface{}{}
	}

	// Convert the terraform value to a value map and traverse all child
	// attributes, assigned their value in the status map.
	terraformValueMap := value.AsValueMap()
	for k, v := range terraformValueMap {
		var val interface{}
		val, err = r.getCRDValueFromTerraformValue(k, &v, schema.Block)
		if err != nil {
			return err
		}
		status[k] = val
	}

	err = unstructured.SetNestedMap(crd.Object, status, crdStatusKeyName)
	if err != nil {
		return fmt.Errorf("Error setting status field: %s", err)
	}

	return nil
}

func (r *TerraformReconciler) planAndApplyConfig(resourceName string, config cty.Value, state *cty.Value) (*cty.Value, error) {
	planResponse := r.provider.Plugin.PlanResourceChange(providers.PlanResourceChangeRequest{
		TypeName:         resourceName,
		PriorState:       *state, // State after last apply or empty if non-existent
		ProposedNewState: config, // Config from CRD representing desired state
		Config:           config, // Config from CRD representing desired state ? Unsure why duplicated but hey ho.
	})

	if err := planResponse.Diagnostics.Err(); err != nil {
		log.Println(err.Error())
		return nil, fmt.Errorf("Failed in Terraform Plan: %s", err)
	}

	applyResponse := r.provider.Plugin.ApplyResourceChange(providers.ApplyResourceChangeRequest{
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

func unflattenTerraformCollectionValue(attrName string, value *cty.Value, nestedBlock *configschema.NestedBlock) (*cty.Value, bool) {
	// Unflatten any flattened terraform values if required
	if value == nil {
		return value, false
	}
	if nestedBlock == nil {
		return value, false
	}
	switch nestedBlock.Nesting {
	case configschema.NestingList:
		v := cty.ListVal([]cty.Value{*value})
		return &v, true
	case configschema.NestingSet:
		v := cty.SetVal([]cty.Value{*value})
		return &v, true
	case configschema.NestingMap:
		v := cty.MapVal(map[string]cty.Value{
			attrName: *value,
		})
		return &v, true
	}
	return value, false
}

func flattenTerraformCollectionValue(value *cty.Value, nestedBlock *configschema.NestedBlock) (*cty.Value, bool) {
	// Unflatten any flattened terraform values if required
	if value == nil {
		return value, false
	}
	if !isTerraformNestedBlockAOpenAPIObjectProperty(nestedBlock) {
		return value, false
	}
	switch nestedBlock.Nesting {
	case configschema.NestingList:
		v := value.AsValueSlice()
		if len(v) == 1 {
			return &v[0], true
		}
		return &cty.EmptyObjectVal, true
	case configschema.NestingSet:
		v := value.AsValueSlice()
		if len(v) == 1 {
			return &v[0], true
		}
		return &cty.EmptyObjectVal, true
	case configschema.NestingMap:
		vm := value.AsValueMap()
		for _, v := range vm {
			return &v, true
		}
		return &cty.EmptyObjectVal, true
	}
	return value, false
}
