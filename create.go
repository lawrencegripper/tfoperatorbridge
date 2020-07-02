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

	for _, resource := range resources {
		output, err := json.MarshalIndent(resource, "", "  ")
		if err != nil {
			panic(err)
		}
		fmt.Printf("Schema: %+v", string(output))
	}
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
	property := getSchemaForType(attributeName, &attribute.Type)
	property.Description = attribute.Description
	// Todo Handle other types
	if schema.Properties == nil {
		schema.Properties = map[string]spec.Schema{}
	}
	schema.Properties[attributeName] = *property
}

func getSchemaForType(name string, item *cty.Type) *spec.Schema {
	// Bulk of mapping from TF Schema -> OpenAPI Schema here.
	// TF Type reference: https://www.terraform.io/docs/extend/schemas/schema-types.html
	// Open API Type reference: https://swagger.io/docs/specification/data-models/data-types/

	var property *spec.Schema
	// Handle basic types - string, bool, number
	if item.Equals(cty.String) {
		property = spec.StringProperty()
	} else if item.Equals(cty.Bool) {
		property = spec.BoolProperty()
	} else if item.Equals(cty.Number) {
		property = spec.Float64Property()

		// Handle more complex types - map, set and list
	} else if item.IsMapType() {
		mapType := getSchemaForType(name+"mapType", item.MapElementType())
		property = spec.MapProperty(mapType)
	} else if item.IsListType() {
		listType := getSchemaForType(name+"listType", item.ListElementType())
		property = spec.ArrayProperty(listType)
	} else if item.IsSetType() {
		setType := getSchemaForType(name+"setType", item.SetElementType())
		property = spec.ArrayProperty(setType)
	} else {
		log.Printf("[Error] Unknown type on attribute. Skipping %v", name)
	}

	return property
}
