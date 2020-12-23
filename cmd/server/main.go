package main

import (
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"github.com/go-openapi/spec"
	"github.com/lawrencegripper/tfoperatorbridge/pkg/k8s"
	"github.com/lawrencegripper/tfoperatorbridge/pkg/tfprovider"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {
	log := ctrl.Log.WithName("main")

	provider, schemas, resources := setupCRDs(log)

	// Start an informer to watch for crd items
	k8s.StartControllerRuntime(provider, resources, schemas)
}

func setupCRDs(log logr.Logger) (*tfprovider.TerraformProvider, []spec.Schema, []k8s.GroupVersionFull) {
	// Get a provider instance by installing or using existing binary
	provider, err := tfprovider.SetupProvider(log)
	if err != nil {
		panic(err)
	}

	// Creating CRDs in K8s with correct structure based on TF Schemas
	resources, schemas, err := k8s.CreateK8sCRDsFromTerraformProvider(getK8sClientConfig(), provider)
	if err != nil {
		panic(err)
	}

	return provider, schemas, resources
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
