package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/providers"
	"github.com/zclconf/go-cty/cty"
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

func reconcileCrd(provider *plugin.GRPCProvider, kind string, crd *unstructured.Unstructured) {

	// Get the kinds terraform schema
	resourceName := "azurerm_" + strings.Replace(kind, "-", "_", -1)
	rgSchema := provider.GetSchema().ResourceTypes[resourceName]

	// Get the spec from the CRD
	spec := crd.Object["spec"].(map[string]interface{})
	jsonSpecRaw, err := json.Marshal(spec)
	if err != nil {
		panic(err)
	}

	// Get any saved state
	var state []byte
	annotations := crd.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if stateString, exists := annotations["tfstate"]; exists {
		if state, err = base64.StdEncoding.DecodeString(stateString); err != nil {
			panic(err)
		}
	} else {
		state = []byte{}
	}

	var configValue *cty.Value

	deleting := false
	metadata, gotMetadata := crd.Object["metadata"].(map[string]interface{})
	if gotMetadata {
		if _, deleting = metadata["deletionTimestamp"]; deleting {
			// Deleting, so set a NullVal for the config
			v := cty.NullVal(rgSchema.Block.ImpliedType())
			configValue = &v
		}
	} else {
		metadata = map[string]interface{}{}
	}
	finalizers, gotFinalizers := metadata["finalizers"].([]string)
	if !gotFinalizers {
		finalizers = []string{}
	}
	gotCRDFinalizer := false
	for _, f := range finalizers {
		if f == "tfoperatorbridge.local" {
			gotCRDFinalizer = true
			break
		}
	}
	if !gotCRDFinalizer {
		log.Println("Adding finalizer")
		finalizers = append(finalizers, "tfoperatorbridge.local")
		metadata["finalizers"] = finalizers
	}
	if configValue == nil {
		// Create a TF cty.Value from the Spec JSON
		configValue = createEmptyResourceValue(rgSchema, "test1")
		configValue, err = applyValuesFromJSON(rgSchema, configValue, string(jsonSpecRaw))
		if err != nil {
			panic(err)
		}
	}

	newState := planAndApplyConfig(provider, resourceName, *configValue, []byte(state))

	annotations["tfstate"] = base64.StdEncoding.EncodeToString(newState)
	gen := crd.GetGeneration()
	annotations["lastAppliedGeneration"] = strconv.FormatInt(gen, 10)
	crd.SetAnnotations(annotations)
	crd.Object["status"] = map[string]interface{}{
		"id": "testing",
	}

	if deleting {
		updatedFinalizers := []string{}
		for _, f := range finalizers {
			if f == "tfoperatorbridge.local" {
				log.Println("Removing finalizer")
			} else {
				updatedFinalizers = append(updatedFinalizers, f)
			}
		}
		metadata["finalizers"] = updatedFinalizers
	}
}

func exampleChangesToResourceGroup(provider *plugin.GRPCProvider) {
	// (Data Source) Example 1: Read an subscription azurerm datasource
	// readSubscriptionDataSource(provider)

	// #1 Create RG
	rgSchema := provider.GetSchema().ResourceTypes["azurerm_resource_group"]
	rgName := "tob" + RandomString(12)
	log.Println(fmt.Sprintf("-------------------> Testing with Resource Group %q", rgName))

	configValue := createEmptyResourceValue(rgSchema, "test1")
	configValue, err := applyValuesFromJSON(rgSchema, configValue, `{
		"name": "`+rgName+`", 
		"location": "westeurope"
	}`)
	if err != nil {
		log.Println(err)
		panic("Failed to get Value from JSON")
	}

	rgState1 := planAndApplyConfig(provider, "azurerm_resource_group", *configValue, []byte{})

	// #2 Update RG with tags
	configValue = createEmptyResourceValue(rgSchema, "test1")
	configValue, err = applyValuesFromJSON(rgSchema, configValue, `{
		"name": "`+rgName+`", 
		"location": "westeurope", 
		"tags" : {
			"testTag": "testTagValue2"
		}
	}`)
	if err != nil {
		log.Println(err)
		panic("Failed to get Value from JSON")
	}
	rgState2 := planAndApplyConfig(provider, "azurerm_resource_group", *configValue, rgState1)

	// #3 Create Storage Account
	storageSchema := provider.GetSchema().ResourceTypes["azurerm_storage_account"]
	storageName := strings.ToLower("tob" + RandomString(12))
	log.Println(fmt.Sprintf("-------------------> Testing with Storage Account %q", rgName))

	configValue = createEmptyResourceValue(storageSchema, "test1")
	configValue, err = applyValuesFromJSON(storageSchema, configValue, `{
		"name": "`+storageName+`",
		"resource_group_name": "`+rgName+`", 
		"location": "westeurope",
		"account_tier": "Standard",
		"account_replication_type": "LRS",
		"is_hns_enabled" : false
	}`)
	if err != nil {
		log.Println(err)
		panic("Failed to get Value from JSON")
	}
	storageState1 := planAndApplyConfig(provider, "azurerm_storage_account", *configValue, []byte{})
	_ = storageState1

	// 4 Delete the RG
	rgNullValueResource := cty.NullVal(rgSchema.Block.ImpliedType())
	rgState3 := planAndApplyConfig(provider, "azurerm_resource_group", rgNullValueResource, rgState2)

	// Todo: Persist the state response from apply somewhere
	_ = rgState3
}

func createEmptyResourceValue(schema providers.Schema, resourceName string) *cty.Value {
	emptyValue := schema.Block.EmptyValue()
	valueMap := emptyValue.AsValueMap()
	valueMap["display_name"] = cty.StringVal(resourceName)
	value := cty.ObjectVal(valueMap)
	return &value
}

func applyValuesFromJSON(schema providers.Schema, originalValue *cty.Value, jsonString string) (*cty.Value, error) {
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

func planAndApplyConfig(provider *plugin.GRPCProvider, resourceName string, config cty.Value, stateSerialized []byte) []byte {
	var state cty.Value
	if len(stateSerialized) == 0 {
		schema := provider.GetSchema().ResourceTypes[resourceName]
		state = schema.Block.EmptyValue()
	} else {
		if err := state.GobDecode(stateSerialized); err != nil {
			log.Println(err)
			panic("Failed to decode state")
		}
	}

	planResponse := provider.PlanResourceChange(providers.PlanResourceChangeRequest{
		TypeName:         resourceName,
		PriorState:       state,  // State after last apply or empty if non-existent
		ProposedNewState: config, // Config from CRD representing desired state
		Config:           config, // Config from CRD representing desired state ? Unsure why duplicated but hey ho.
	})

	if planResponse.Diagnostics.Err() != nil {
		log.Println(planResponse.Diagnostics.Err().Error())
		panic("Failed planning resource")
	}

	applyResponse := provider.ApplyResourceChange(providers.ApplyResourceChangeRequest{
		TypeName:     resourceName,              // Working theory:
		PriorState:   state,                     // This is the state from the .tfstate file before the apply is made
		Config:       config,                    // The current HCL configuration or what would be in your terraform file
		PlannedState: planResponse.PlannedState, // The result of a plan (read / diff) between HCL Config and actual resource state
	})
	if applyResponse.Diagnostics.Err() != nil {
		log.Println(applyResponse.Diagnostics.Err().Error())
		panic("Failed applying resourceGroup")
	}

	resultState, err := applyResponse.NewState.GobEncode()

	if err != nil {
		log.Println(err)
		panic("Failed to encode state")
	}

	return resultState
}

func readSubscriptionDataSource(provider *plugin.GRPCProvider) {
	// Now lets use the provider to read from `azurerm_subscription` data source
	// First lets get the Schema for the datasource.
	subDataSourceSchema := provider.GetSchema().DataSources["azurerm_subscription"]
	// Now lets get an empty value map which represents that schema with empty attributes
	subConfigValueMap := subDataSourceSchema.Block.EmptyValue().AsValueMap()
	// Then lets give the data source a display name as this is the only required field here.
	// NOTE: display name is the section following the resource declaration in HCL
	// data "azurerm_subscription" "display_name" here
	subConfigValueMap["display_name"] = cty.StringVal("testing1")

	// Then package this back up as an objectVal and submit it to the provider
	readResp := provider.ReadDataSource(providers.ReadDataSourceRequest{
		TypeName: "azurerm_subscription",
		Config:   cty.ObjectVal(subConfigValueMap),
	})

	// Check it didn't error.
	if readResp.Diagnostics.Err() != nil {
		log.Println(readResp.Diagnostics.Err().Error())
		panic("Failed reading subscription")
	}

	log.Println("Read subscription data")
	log.Println(readResp.State)

}

func RandomString(n int) string {
	rand.Seed(time.Now().UnixNano())

	var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

	s := make([]rune, n)
	for i := range s {
		s[i] = letters[rand.Intn(len(letters))]
	}
	return string(s)
}
