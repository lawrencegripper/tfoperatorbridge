package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/go-openapi/spec"
	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/plugin/discovery"
	"github.com/zclconf/go-cty/cty"
)

func main() {
	pluginMeta := discovery.FindPlugins(plugin.ProviderPluginName, []string{"./hack/.terraform/plugins/linux_amd64/"}).WithName("azurerm")

	if pluginMeta.Count() < 1 {
		panic("no plugins found")
	}
	pluginClient := plugin.Client(pluginMeta.Newest())
	rpcClient, err := pluginClient.Client()
	if err != nil {
		panic(fmt.Errorf("Failed to initialize plugin: %s", err))
	}
	// create a new resource provisioner.
	raw, err := rpcClient.Dispense(plugin.ProviderPluginName)
	if err != nil {
		panic(fmt.Errorf("Failed to dispense plugin: %s", err))
	}

	provider := raw.(*plugin.GRPCProvider)

	tfSchema := provider.GetSchema()
	// fmt.Printf("provider schemea: %+v", tfSchema)

	resources := []spec.Schema{}
	objType := spec.StringOrArray{"object"}
	for resourceName, resource := range tfSchema.ResourceTypes {

		// Create objects for both the spec and status blocks
		specCRD := spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type:     objType,
				Required: []string{},
			},
		}
		statusCRD := spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type:     objType,
				Required: []string{},
			},
		}

		addBlockToSchema(&statusCRD, &specCRD, "root", resource.Block)

		def := spec.Schema{}
		def.Type = objType
		// Compose these into a top level object
		def.Properties = map[string]spec.Schema{
			"spec":   specCRD,
			"status": statusCRD,
		}
		def.Description = resourceName

		resources = append(resources, def)
	}

	// Get schema
	output, err := json.Marshal(resources[0])
	if err != nil {
		panic(err)
	}
	fmt.Printf("Schema: %+v", string(output))
}

func addBlockToSchema(statusCRD, specCRD *spec.Schema, blockName string, block *configschema.Block) {
	for attributeName, attribute := range block.Attributes {
		// Computer attributes from Terraform map to the `status` block in K8s CRDS
		if attribute.Computed {
			if attribute.Required {
				statusCRD.Required = append(statusCRD.Required, attributeName)
			}
			addAttributeToSchema(statusCRD, attributeName, attribute)
		} else {
			// All other attributes are for the `spec` block
			if attribute.Required {
				specCRD.Required = append(specCRD.Required, attributeName)
			}
			addAttributeToSchema(specCRD, attributeName, attribute)
		}
	}
}

func addAttributeToSchema(schema *spec.Schema, attributeName string, attribute *configschema.Attribute) {
	var property spec.Schema
	// Handle String property
	if attribute.Type.Equals(cty.String) {
		property = *spec.StringProperty()
	} else if attribute.Type.Equals(cty.Bool) {
		property = *spec.BoolProperty()
	} else if attribute.Type.Equals(cty.Number) {
		property = *spec.Float64Property()
	} else if attribute.Type.IsMapType() {
		fmt.Println("How do I map?")
	} else if attribute.Type.IsListType() {
		fmt.Println("How do I list?")
	} else if attribute.Type.IsSetType() {
		fmt.Println("How do I set?")
	} else {
		log.Printf("[Error] Unknown type on attribute. Skipping %v", attributeName)
	}
	property.Description = attribute.Description
	// Todo Handle other types
	if schema.Properties == nil {
		schema.Properties = map[string]spec.Schema{}
	}
	schema.Properties[attributeName] = property
}
