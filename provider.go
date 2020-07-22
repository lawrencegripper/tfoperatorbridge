package main

import (
	"fmt"
	"log"
	"os"

	"github.com/go-logr/logr"
	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/lang"
	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/plugin/discovery"
	"github.com/hashicorp/terraform/providers"
	"github.com/zclconf/go-cty/cty"
)

func getInstanceOfProvider(providerName string) *plugin.GRPCProvider {
	pluginMeta := discovery.FindPlugins(plugin.ProviderPluginName, []string{"./hack/.terraform/plugins/linux_amd64/"}).WithName(providerName)

	if pluginMeta.Count() < 1 {
		panic("no plugins found")
	}
	clientConfig := plugin.ClientConfig(pluginMeta.Newest())

	// Don't log provider details unless provider log is enabled by env
	if _, exists := os.LookupEnv("ENABLE_PROVIDER_LOG"); !exists {
		clientConfig.Logger = hclog.NewNullLogger()
	}
	pluginClient := goplugin.NewClient(clientConfig)

	rpcClient, err := pluginClient.Client()

	if err != nil {
		panic(fmt.Errorf("Failed to initialize plugin: %s", err))
	}
	// create a new resource provisioner.
	raw, err := rpcClient.Dispense(plugin.ProviderPluginName)
	if err != nil {
		panic(fmt.Errorf("Failed to dispense plugin: %s", err))
	}
	return raw.(*plugin.GRPCProvider)
}

func createEmptyProviderConfWithDefaults(provider *plugin.GRPCProvider, configBody string) (*cty.Value, error) {
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
	providerConfigBlock := provider.GetSchema().Provider.Block

	// Parse the content of the provider block given to us into a body.
	file, diagParse := hclsyntax.ParseConfig([]byte(configBody), "test.tf", hcl.Pos{})
	if diagParse.HasErrors() {
		return nil, fmt.Errorf("Failed parsing provider config block: %s", diagParse.Error())
	}

	scope := lang.Scope{}
	expandedConf, diags := scope.ExpandBlock(file.Body, providerConfigBlock)
	if diags.Err() != nil {
		return nil, fmt.Errorf("Failed expanding provider config block: %w", diags.Err())
	}
	configFull, diags := scope.EvalBlock(expandedConf, providerConfigBlock)
	if diags.Err() != nil {
		return nil, fmt.Errorf("Failed evaluating provider config block: %w", diags.Err())
	}

	// Call the `PrepareProviderConfig` with the config object. This returns a version of that config with the
	// required default setup as `PreparedConfig` under the response object.
	// Warning: Diagnostics houses errors, the typical go err pattern isn't followed - must check `resp.Diagnostics.Err()`
	prepConfigResp := provider.PrepareProviderConfig(providers.PrepareProviderConfigRequest{
		Config: configFull,
	})
	if err := prepConfigResp.Diagnostics.Err(); err != nil {
		return nil, fmt.Errorf("Failed to set defaults on provider config: %w", err)
	}

	return &configFull, nil
}

func configureProvider(log logr.Logger, provider *plugin.GRPCProvider) {
	configWithDefaults, err := createEmptyProviderConfWithDefaults(provider, "")
	if err != nil {
		log.Error(err, fmt.Sprintf("Failed to prepare config: %s", err))
		panic("Failed to prepare config")
	}

	configValueMap := configWithDefaults.AsValueMap()
	// Todo: populate these values from configmap
	// Lets set the values we need to set while we have the value map
	configValueMap["client_id"] = cty.StringVal(os.Getenv("ARM_CLIENT_ID"))
	configValueMap["client_secret"] = cty.StringVal(os.Getenv("ARM_CLIENT_SECRET"))
	configValueMap["tenant_id"] = cty.StringVal(os.Getenv("ARM_TENANT_ID"))
	configValueMap["subscription_id"] = cty.StringVal(os.Getenv("ARM_SUBSCRIPTION_ID"))

	// Now we have a prepared config we can configure the provider.
	// Warning (again): Diagnostics houses errors, the typical go err pattern isn't followed - must check `resp.Diagnostics.Err()`
	configureProviderResp := provider.Configure(providers.ConfigureRequest{
		Config: cty.ObjectVal(configValueMap),
	})
	if err := configureProviderResp.Diagnostics.Err(); err != nil {
		log.Error(err, fmt.Sprintf("Failed to configure provider: %s", err))
		panic(fmt.Sprintf("Failed to configure provider: %s", err))
	}
}

// This compliments the `emtypBlock` as it will check that blocks are correctly populated
// when a single block is mandated (min 1 max 1)
func populateSingleInstanceBlocks(value cty.Value, blocks map[string]*configschema.NestedBlock) cty.Value {

	valueMap := value.AsValueMap()
	for name, nestedBlock := range blocks {
		log.Println("NestedBlock: " + name)
		if nestedBlock.MinItems == 1 && nestedBlock.MaxItems == 1 {
			// Create an array of length 1 with the empty block values as required by schema

			// Recurse into the block to see if any other min/max 1 block exist and populate those
			if len(nestedBlock.BlockTypes) > 0 {
				log.Println("Nested block type has nested blocks:" + name)
				result := populateSingleInstanceBlocks(nestedBlock.EmptyValue(), nestedBlock.BlockTypes)
				populatedBlockValue := cty.ListVal([]cty.Value{result})
				valueMap[name] = populatedBlockValue
			} else {
				// For blocks without nested block bring back a empty list val
				valueMap[name] = cty.ListVal([]cty.Value{nestedBlock.EmptyValue()})
			}
		} else {
			// If they have no nested blocks set an empty value, handle the case that
			// the provided valueMap may be nil
			if valueMap == nil {
				valueMap = map[string]cty.Value{}
			}
			valueMap[name] = nestedBlock.EmptyValue()
		}
	}
	return cty.ObjectVal(valueMap)
}
