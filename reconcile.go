package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/providers"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func configureProvider(provider *plugin.GRPCProvider) {
	providerConfigBlock := provider.GetSchema().Provider.Block

	// We need a set of cty.Value which maps to the schema of the provider's configuration block.
	// NOTE:
	// 1. If the schema has optional elements they're NOT optional in the cty.Value. The cty.Value structure must include all fields
	//    specified in the schema. The values of the attributes can be empy if they're optional. To get this we use `EmptyValue` on the schema
	//    this iterates the schema and creates a `cty.ObjectVal` which maps to the schema with each attribute set to empty.
	// 2. If the schema includes a List item with a min 1 length the `EmptyValue` will no create a valid ObjectVal for the schema.
	//    It will create an empty list item `[]stringval{}` as this doesn't have 1 item it doesn't match the schema. What is needed is a list with 1 item.
	//    When these items are missing the error messages are of the format `features attribute is required`
	// 3. When the `cty.ObjectVal` doesn't follow the required schema the error messages provided back don't make this immediately clear.
	//    You may for example receive a message of `attribute 'use_msi' bool is required` when the error was introducing the wrong structure for the `features` list
	configProvider := providerConfigBlock.EmptyValue()

	// Here is an example of a list min 1.
	// The `features` block in the Azure RM provider
	//
	// provider "azurerm" {
	// 	version = "=2.0.0"
	// 	features {}
	// }
	//
	// Represented as YAML this would be:
	//
	// features:
	// - ~

	// Workaround to create a `cty.ListVal` for `features` block with one blank item in it.
	// Get block definition
	featuresType := providerConfigBlock.BlockTypes["features"]
	// Create a map to represent the block
	featuresBlockMap := map[string]cty.Value{}
	log.Println(featuresType)
	// Get each of the nested blocks required in the block and create
	// empty items for them. Insert them into the featuresBlockMap
	for name, nestedBlock := range featuresType.BlockTypes {
		featuresBlockMap[name] = nestedBlock.EmptyValue()
	}
	configValueMap := configProvider.AsValueMap()
	// On the provider config block set the `features` attribute to be a list with an instance of the features block in it.
	configValueMap["features"] = cty.ListVal([]cty.Value{cty.ObjectVal(featuresBlockMap)})

	configFull := cty.ObjectVal(configValueMap)

	// Call the `PrepareProviderConfig` with the config object. This returns a version of that config with the
	// required default setup as `PreparedConfig` under the response object.
	// Warning: Diagnostics houses errors, the typical go err pattern isn't followed - must check `resp.Diagnostics.Err()`
	prepConfigResp := provider.PrepareProviderConfig(providers.PrepareProviderConfigRequest{
		Config: configFull,
	})
	if prepConfigResp.Diagnostics.Err() != nil {
		log.Println(prepConfigResp.Diagnostics.Err().Error())
		panic("Failed to prepare config")
	}

	// Lets set the values we need to set while we have the value map
	configValueMap = prepConfigResp.PreparedConfig.AsValueMap()
	configValueMap["client_id"] = cty.StringVal(os.Getenv("ARM_CLIENT_ID"))
	configValueMap["client_secret"] = cty.StringVal(os.Getenv("ARM_CLIENT_SECRET"))
	configValueMap["tenant_id"] = cty.StringVal(os.Getenv("ARM_TENANT_ID"))
	configValueMap["subscription_id"] = cty.StringVal(os.Getenv("ARM_SUBSCRIPTION_ID"))

	// Now we have a prepared config we can configure the provider.
	// Warning (again): Diagnostics houses errors, the typical go err pattern isn't followed - must check `resp.Diagnostics.Err()`
	configureProviderResp := provider.Configure(providers.ConfigureRequest{
		Config: cty.ObjectVal(configValueMap),
	})
	if configureProviderResp.Diagnostics.Err() != nil {
		log.Println(configureProviderResp.Diagnostics.Err().Error())
		panic("Failed to configure provider")
	}
}

// TODO remove the usage of reconcileCrd in informer and switch to reconcileCrdInternal
func reconcileCrd(provider *plugin.GRPCProvider, crd *unstructured.Unstructured) {
	if err := reconcileCrdInternal(provider, crd); err != nil {
		panic(err)
	}
}
func reconcileCrdInternal(provider *plugin.GRPCProvider, crd *unstructured.Unstructured) error {

	// Get the kinds terraform schema
	kind := crd.GetKind()
	resourceName := "azurerm_" + strings.Replace(kind, "-", "_", -1)
	schema := provider.GetSchema().ResourceTypes[resourceName]

	var configValue *cty.Value
	var deleting bool
	if deleting = isDeleting(crd); deleting {
		// Deleting, so set a NullVal for the config
		v := cty.NullVal(schema.Block.ImpliedType())
		configValue = &v
	} else {
		ensureFinalizer(crd)

		// Get the spec from the CRD
		spec := crd.Object["spec"].(map[string]interface{})
		jsonSpecRaw, err := json.Marshal(spec)
		if err != nil {
			return fmt.Errorf("Error marshalling the CRD spec to JSON: %s", err)
		}

		// Create a TF cty.Value from the Spec JSON
		configValue = createEmptyResourceValue(schema, "test1")
		configValue, err = applySpecValuesToTerraformConfig(schema, configValue, string(jsonSpecRaw))
		if err != nil {
			return fmt.Errorf("Error applying values from the CRD spec to Terraform config: %s", err)
		}
	}

	state, err := getTerraformState(crd)
	if err != nil {
		return fmt.Errorf("Error getting Terraform state from CRD: %s", err)
	}
	newState, err := planAndApplyConfig(provider, resourceName, *configValue, []byte(state))
	if err != nil {
		return fmt.Errorf("Error applying changes in Terraform: %s", err)
	}

	if err := saveTerraformState(crd, newState); err != nil {
		return fmt.Errorf("Error saving Terraform state to CRD: %s", err)
	}
	saveLastAppliedGeneration(crd)

	if deleting {
		removeFinalizer(crd)
	} else {
		// TODO - perform mapping of newState onto the CRD Status (currently hacking ID in for testing)
		newStateMap := newState.AsValueMap()
		crd.Object["status"] = map[string]interface{}{
			"id": newStateMap["id"].AsString(),
		}
	}
	return nil
}

func isDeleting(resource *unstructured.Unstructured) bool {
	deleting := false
	metadata, gotMetadata := resource.Object["metadata"].(map[string]interface{})
	if gotMetadata {
		if _, deleting = metadata["deletionTimestamp"]; deleting {
			return true
		}
	}
	return false
}
func ensureFinalizer(resource *unstructured.Unstructured) {
	metadata, gotMetadata := resource.Object["metadata"].(map[string]interface{})
	if !gotMetadata {
		metadata = map[string]interface{}{}
	}
	finalizers, gotFinalizers := metadata["finalizers"].([]string)
	if !gotFinalizers {
		finalizers = []string{}
	}
	for _, f := range finalizers {
		if f == "tfoperatorbridge.local" {
			// already present
			return
		}
	}
	finalizers = append(finalizers, "tfoperatorbridge.local")
	metadata["finalizers"] = finalizers
	resource.Object["metadata"] = metadata
}
func removeFinalizer(resource *unstructured.Unstructured) {
	metadata, gotMetadata := resource.Object["metadata"].(map[string]interface{})
	if !gotMetadata {
		metadata = map[string]interface{}{}
	}
	finalizers, gotFinalizers := metadata["finalizers"].([]string)
	if !gotFinalizers {
		finalizers = []string{}
	}
	updatedFinalizers := []string{}
	for _, f := range finalizers {
		if f == "tfoperatorbridge.local" {
			log.Println("Removing finalizer")
		} else {
			updatedFinalizers = append(updatedFinalizers, f)
		}
	}
	metadata["finalizers"] = updatedFinalizers
	resource.Object["metadata"] = metadata
}
func getTerraformState(resource *unstructured.Unstructured) ([]byte, error) {
	annotations := resource.GetAnnotations()
	if annotations != nil {
		if stateString, exists := annotations["tfstate"]; exists {
			state, err := base64.StdEncoding.DecodeString(stateString)
			if err != nil {
				return []byte{}, err
			}
			return state, nil
		}
	}
	return []byte{}, nil
}
func saveTerraformState(resource *unstructured.Unstructured, state *cty.Value) error {
	serializedState, err := ctyjson.Marshal(*state, state.Type())
	if err != nil {
		return err
	}
	annotations := resource.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["tfstate"] = base64.StdEncoding.EncodeToString(serializedState)
	resource.SetAnnotations(annotations)
	return nil
}
func saveLastAppliedGeneration(resource *unstructured.Unstructured) {
	gen := resource.GetGeneration()
	annotations := resource.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["lastAppliedGeneration"] = strconv.FormatInt(gen, 10)
	resource.SetAnnotations(annotations)
}
func createEmptyResourceValue(schema providers.Schema, resourceName string) *cty.Value {
	emptyValue := schema.Block.EmptyValue()
	valueMap := emptyValue.AsValueMap()
	valueMap["display_name"] = cty.StringVal(resourceName)
	value := cty.ObjectVal(valueMap)
	return &value
}

func applySpecValuesToTerraformConfig(schema providers.Schema, originalValue *cty.Value, jsonString string) (*cty.Value, error) {
	valueMap := originalValue.AsValueMap()

	// TODO - track the json fields that are accessed so that we can return an error if there
	// are any that weren't visited, i.e. not defined in the schema

	var jsonData map[string]interface{}
	if err := json.Unmarshal([]byte(jsonString), &jsonData); err != nil {
		return nil, fmt.Errorf("Error unmarshalling JSON data: %s", err)
	}

	for name, attribute := range schema.Block.Attributes {
		jsonVal, gotJsonVal := jsonData[name]
		if gotJsonVal {
			v, err := getValue(attribute.Type, jsonVal)
			if err != nil {
				return nil, fmt.Errorf("Error getting value for %q: %s", name, err)
			}
			valueMap[name] = *v
		}
	}

	// TODO handle schema.Block.BlockTypes

	newValue := cty.ObjectVal(valueMap)
	return &newValue, nil
}

func getValue(t cty.Type, value interface{}) (*cty.Value, error) {
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
			mapValue, err := getValue(*elementType, v)
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

func planAndApplyConfig(provider *plugin.GRPCProvider, resourceName string, config cty.Value, stateSerialized []byte) (*cty.Value, error) {
	schema := provider.GetSchema().ResourceTypes[resourceName]
	var state cty.Value
	if len(stateSerialized) == 0 {
		state = schema.Block.EmptyValue()
	} else {
		unmashaledState, err := ctyjson.Unmarshal(stateSerialized, schema.Block.ImpliedType())
		if err != nil {
			log.Println(err)
			panic("Failed to decode state")
		}
		state = unmashaledState
	}

	planResponse := provider.PlanResourceChange(providers.PlanResourceChangeRequest{
		TypeName:         resourceName,
		PriorState:       state,  // State after last apply or empty if non-existent
		ProposedNewState: config, // Config from CRD representing desired state
		Config:           config, // Config from CRD representing desired state ? Unsure why duplicated but hey ho.
	})

	if err := planResponse.Diagnostics.Err(); err != nil {
		log.Println(err.Error())
		return nil, fmt.Errorf("Failed in Terraform Plan: %s", err)
	}

	applyResponse := provider.ApplyResourceChange(providers.ApplyResourceChangeRequest{
		TypeName:     resourceName,              // Working theory:
		PriorState:   state,                     // This is the state from the .tfstate file before the apply is made
		Config:       config,                    // The current HCL configuration or what would be in your terraform file
		PlannedState: planResponse.PlannedState, // The result of a plan (read / diff) between HCL Config and actual resource state
	})
	if err := applyResponse.Diagnostics.Err(); err != nil {
		log.Println(err.Error())
		return nil, fmt.Errorf("Failed in Terraform Apply: %s", err)
	}

	return &applyResponse.NewState, nil
}
