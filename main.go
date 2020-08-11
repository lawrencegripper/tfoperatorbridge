package main

import (
	"os"
	"path/filepath"

	"github.com/lawrencegripper/tfoperatorbridge/tfprovider"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {
	log := ctrl.Log.WithName("main")

	// Get a provider instance by installing or using existing binary
	provider, err := tfprovider.SetupProvider(log)
	if err != nil {
		panic(err)
	}

	// Creating CRDs in K8s with correct structure based on TF Schemas
	resources, schemas, err := createK8sCRDsFromTerraformProvider(provider)
	if err != nil {
		panic(err)
	}

	// Start an informer to watch for crd items
	setupControllerRuntime(provider, resources, schemas)
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
