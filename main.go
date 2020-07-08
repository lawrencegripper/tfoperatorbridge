package main

import (
	"fmt"

	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/plugin/discovery"
)

func main() {
	provider := getInstanceOfAzureRMProvider()

	// Example talking to the Azure resources using the provider
	useProviderToTalkToAzure(provider)

	// Example creating CRDs in K8s with correct structure based on TF Schemas
	// createCRDsForResources(provider)

}

func getInstanceOfAzureRMProvider() *plugin.GRPCProvider {
	providerName := "azurerm"
	pluginMeta := discovery.FindPlugins(plugin.ProviderPluginName, []string{"./hack/.terraform/plugins/linux_amd64/"}).WithName(providerName)

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
	return raw.(*plugin.GRPCProvider)
}
