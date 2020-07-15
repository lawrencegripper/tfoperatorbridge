package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/plugin/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	provider := getInstanceOfAzureRMProvider()

	// Example talking to the Azure resources using the provider
	configureProvider(provider)
	// exampleChangesToResourceGroup(provider)

	// Example creating CRDs in K8s with correct structure based on TF Schemas
	resources := createCRDsForResources(provider)
	// startSharedInformer(provider, resources)

	// Start an informer to watch for crd items
	setupControllerRuntime(provider, resources)
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

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func getK8sClientConfig() *rest.Config {
	home := homeDir()
	kubeConfigPath := filepath.Join(home, ".kube", "config")

	envKubeConfig := os.Getenv("KUBECONFIG")
	if envKubeConfig != "" {
		kubeConfigPath = envKubeConfig
	}
	// use the current context in kubeconfig
	clientConfig, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		panic(err.Error())
	}

	return clientConfig
}
