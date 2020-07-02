package main

import (
	"fmt"

	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/plugin/discovery"
)

func main() {
	pluginMeta := discovery.FindPlugins(plugin.ProviderPluginName, []string{"/root/.terraform.d/plugins"}).WithName("databricks")

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

	schema := provider.GetSchema()
	fmt.Printf("provider schemea: %+v", schema)
	// Get schema
}
