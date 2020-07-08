package main

import (
	"log"
	"os"

	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/providers"
	"github.com/zclconf/go-cty/cty"
)

func reconcile(provider *plugin.GRPCProvider) {
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

	// Now in theory we can use it...

	readResp := provider.ReadDataSource(providers.ReadDataSourceRequest{
		TypeName: "azurerm_subscription",
		Config: cty.ObjectVal(map[string]cty.Value{
			"display_name": cty.StringVal("test"),
			"id":           cty.StringVal("bob"),
			"state":        cty.ObjectVal(map[string]cty.Value{}),
		}),
	})

	if readResp.Diagnostics.Err() != nil {
		log.Println(readResp.Diagnostics.Err().Error())
		panic("end")
	}
}
