package main_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Azure/azure-sdk-for-go/services/resources/mgmt/2019-05-01/resources"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure/auth"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var k8sClient dynamic.Interface
var azureResourceGroupsClient *resources.GroupsClient
var azureResourcesClient *resources.Client

func TestTfOperatorBridge(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Workspace Suite")
}

var _ = BeforeSuite(func() {
	var err error
	// K8sClient used for kubernetes
	k8sClient, err = configureK8sClient()
	Expect(err).To(BeNil())
	Expect(k8sClient).ToNot(BeNil())

	// azureResourceGroupsClient used for azure resource groups
	azureResourceGroupsClient, err = configureAzureResourceGroupsClient()
	Expect(err).To(BeNil())
	Expect(azureResourceGroupsClient).ToNot(BeNil())

	// azureResourcesClient used for azure resources
	azureResourcesClient, err = configureAzureResourcesClient()
	Expect(err).To(BeNil())
	Expect(azureResourcesClient).ToNot(BeNil())
}, 60)

func configureK8sClient() (dynamic.Interface, error) {
	var kubeconfig string
	if home := os.Getenv("HOME"); home != "" {
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}

	k8sClient, err = dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return k8sClient, nil
}

func configureAzureResourcesClient() (*resources.Client, error) {
	azureSubID, err := GetAzureSubscriptionIDFromEnvironmentSettings()
	if err != nil {
		return nil, err
	}

	azureResourcesClient := resources.NewClient(azureSubID)
	authorizer, err := GetAzureAuthorizerFromEnvrionmentSettings(azureSubID)
	if err != nil {
		return nil, err
	}
	azureResourcesClient.Authorizer = authorizer
	return &azureResourcesClient, nil
}

func configureAzureResourceGroupsClient() (*resources.GroupsClient, error) {
	azureSubID, err := GetAzureSubscriptionIDFromEnvironmentSettings()
	if err != nil {
		return nil, err
	}

	azureResourceGroupsClient := resources.NewGroupsClient(azureSubID)
	authorizer, err := GetAzureAuthorizerFromEnvrionmentSettings(azureSubID)
	if err != nil {
		return nil, err
	}
	azureResourceGroupsClient.Authorizer = authorizer
	return &azureResourceGroupsClient, nil
}

func GetAzureSubscriptionIDFromEnvironmentSettings() (string, error) {
	var azureSubID string
	// Try the env var name used by the azure-sdk-for-go
	if azureSubID = os.Getenv("AZURE_SUBSCRIPTION_ID"); azureSubID == "" {
		// Try the env var name used by the terraform azurerm provider
		if azureSubID = os.Getenv("ARM_SUBSCRIPTION_ID"); azureSubID == "" {
			// Cannot find the subscription id
			return "", fmt.Errorf("neither AZURE_SUBSCRIPTION_ID or ARM_SUBSCRIPTION_ID are not set")
		}
	}
	return azureSubID, nil
}

func GetAzureAuthorizerFromEnvrionmentSettings(azureSubID string) (autorest.Authorizer, error) {
	var authorizer autorest.Authorizer
	// Try the env var names used by the azure-sdk-for-go
	settings, err := auth.GetSettingsFromEnvironment()
	if err != nil {
		return nil, err
	}
	if _, err = settings.GetClientCredentials(); err == nil {
		return settings.GetAuthorizer()
	}
	// Try the env vars names used by the terraform azurerm provider
	var azureClientID string
	var azureClientSecret string
	var azureTenantID string
	if azureClientID = os.Getenv("ARM_CLIENT_ID"); azureClientID == "" {
		return nil, fmt.Errorf("neither AZURE_CLIENT_ID or ARM_CLIENT_ID are not set")
	}
	if azureClientSecret = os.Getenv("ARM_CLIENT_SECRET"); azureClientSecret == "" {
		return nil, fmt.Errorf("neither AZURE_CLIENT_SECRET or ARM_CLIENT_SECRET are not set")
	}
	if azureTenantID = os.Getenv("ARM_TENANT_ID"); azureTenantID == "" {
		return nil, fmt.Errorf("neither AZURE_TENANT_ID or ARM_TENANT_ID are not set")
	}
	authorizer, err = auth.NewClientCredentialsConfig(azureClientID, azureClientSecret, azureTenantID).Authorizer()
	if err != nil {
		return nil, err
	}
	return authorizer, nil
}
