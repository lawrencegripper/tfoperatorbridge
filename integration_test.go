package main_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/resources/mgmt/2019-05-01/resources"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// TODO: Export from package after refactor
const (
	encryptionKeyEnvVar = "TF_STATE_ENCRYPTION_KEY"
)

var _ = Describe("Azure resource creation via CRD", func() {
	Context("with respect to resource dependencies", func() {
		randomString := RandomString(12)
		resourceGroupName := "tftest-" + randomString
		resourceGroupLocation := "westeurope"
		storageAccountName := randomString
		storageAccountLocation := "westeurope"

		gvrResourceGroup := schema.GroupVersionResource{
			Group:    "azurerm.tfb.local",
			Version:  "valpha1",
			Resource: "resource-groups",
		}
		gvrStorageAccount := schema.GroupVersionResource{
			Group:    "azurerm.tfb.local",
			Version:  "valpha1",
			Resource: "storage-accounts",
		}

		When("creating an azure resource group CRD", func() {
			azureResourecGroupCRDRequest := GetAzureResourceGroupCRD(resourceGroupName, resourceGroupLocation)
			var azureResourceGroupCRDResponse *unstructured.Unstructured
			var resourceGroupID string
			It("should create without error", func() {
				_, err := k8sClientWithDefaults(gvrResourceGroup).Create(context.TODO(), &azureResourecGroupCRDRequest, metav1.CreateOptions{})
				Expect(err).To(BeNil())
			}, 30)
			It("should eventually create the azure resource group resource in azure", func() {
				Eventually(func() error {
					_, err := GetAzureResourceGroup(context.TODO(), resourceGroupName)
					return err
				}, time.Second*30, time.Second*5).Should(BeNil())
			}, 30)
			It("should eventually have a status.id property set", func() {
				Eventually(func() bool {
					var err error
					azureResourceGroupCRDResponse, err = k8sClientWithDefaults(gvrResourceGroup).Get(context.TODO(), resourceGroupName, metav1.GetOptions{})
					Expect(err).To(BeNil())
					status, _ := azureResourceGroupCRDResponse.Object["status"].(map[string]interface{})
					var ok bool
					resourceGroupID, ok = status["id"].(string)
					return ok
				}, time.Second*30, time.Second*5).Should(BeTrue(), "CRD should have status.id property")
			}, 30)
			It("should have a status.id property set to a valid azure resource id", func() {
				Expect(resourceGroupID).To(MatchRegexp("/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resourceGroups/" + resourceGroupName))
			}, 30)
			It("should store terraform state encrypted in status._tfoperator.tfState", func() {
				if !EncryptionKeyIsSet() {
					Skip("encryption key is not set, skipping test")
				}
				tfStateString, gotTfState, err := unstructured.NestedString(azureResourceGroupCRDResponse.Object, "status", "_tfoperator", "tfState")
				Expect(err).To(BeNil())
				Expect(gotTfState).To(BeTrue(), "CRD should have status._tfoperator.tfState property")

				var js string
				err = json.Unmarshal([]byte(tfStateString), &js)
				Expect(err).ToNot(BeNil(), "status._tfoperator.tfState property should be encrypted and not valid JSON")

				Expect(len(tfStateString)).ToNot(Equal(0))
				_, err = base64.StdEncoding.DecodeString(tfStateString)
				Expect(err).To(BeNil(), "status._tfoperator.tfState should be base64 encoded")
				// TODO: Once refactored to use packages and the decryption function is exported
				// we can validate that this data is encrypted with the encryption key.
			}, 30)
			It("should store the terraform provider name", func() {
				tfProviderName, gotTfProviderName, err := unstructured.NestedString(azureResourceGroupCRDResponse.Object, "status", "_tfoperator", "providerName")
				Expect(err).To(BeNil())
				Expect(gotTfProviderName).To(BeTrue(), "CRD should have status._tfoperator.providerName property")
				Expect(tfProviderName).To(Equal("azurerm"))
			}, 30)
			It("should store the terraform provider version", func() {
				tfVersion, gotTfVersion, err := unstructured.NestedString(azureResourceGroupCRDResponse.Object, "status", "_tfoperator", "providerVersion")
				Expect(err).To(BeNil())
				Expect(gotTfVersion).To(BeTrue(), "CRD should have status._tfoperator.providerVersion property")
				Expect(tfVersion).To(Equal("2.22.0"))
			}, 30)
			It("should store the terraform provider checksum", func() {
				tfChecksumSHA256, gotTfChecksumSHA256, err := unstructured.NestedString(azureResourceGroupCRDResponse.Object, "status", "_tfoperator", "providerChecksumSHA256")
				Expect(err).To(BeNil())
				Expect(gotTfChecksumSHA256).To(BeTrue(), "CRD should have status._tfoperator.providerChecksumSHA256 property")
				Expect(tfChecksumSHA256).To(Equal("c96318798eca449bfb1fe7ca0017ac9a9f719ed969fbfdd87032da4874e5a5a6"))
			}, 30)
		})
		When("creating an azure storage account CRD", func() {
			azureStorageAccountCRDRequest := GetAzureStorageAccountCRD(resourceGroupName, storageAccountName, storageAccountLocation)
			var azureStorageAccountCRDResponse *unstructured.Unstructured
			var status map[string]interface{}
			var storageAccountID string
			var storageAccountResource resources.GenericResource
			It("should create without error", func() {
				_, err := k8sClientWithDefaults(gvrStorageAccount).Create(context.TODO(), &azureStorageAccountCRDRequest, metav1.CreateOptions{})
				Expect(err).To(BeNil())
			}, 30)
			It("should eventually create the azure storage account resource in azure", func() {
				Eventually(func() error {
					var err error
					storageAccountResource, err = GetAzureResource(context.TODO(),
						resourceGroupName,
						"Microsoft.Storage",
						"storageAccounts",
						storageAccountName,
						"2019-06-01",
					)
					return err
				}, time.Second*30, time.Second*5).Should(BeNil())
			}, 30)
			It("should have the status.id property set", func() {
				Eventually(func() bool {
					var err error
					azureStorageAccountCRDResponse, err = k8sClientWithDefaults(gvrStorageAccount).Get(context.TODO(), storageAccountName, metav1.GetOptions{})
					Expect(err).To(BeNil())
					status, _ = azureStorageAccountCRDResponse.Object["status"].(map[string]interface{})
					var ok bool
					storageAccountID, ok = status["id"].(string)
					return ok
				}, time.Second*30, time.Second*5).Should(BeTrue())
			}, 30)
			It("should have the status.id property set to a valid azure resource id", func() {
				Expect(storageAccountID).To(MatchRegexp("/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resourceGroups/" + resourceGroupName + "/providers/Microsoft.Storage/storageAccounts/" + storageAccountName))
			}, 30)
			It("should have the status.name property set to a non empty string", func() {
				var ok bool
				name, ok := status["name"].(string)
				Expect(ok).To(BeTrue(), "CRD should have a status.name property")
				Expect(name).ToNot(BeEmpty(), "status.name should not be an empty string")
			}, 30)
			It("should have the status.primary_access_key property set to a non empty string", func() {
				var ok bool
				primaryAccessKey, ok := status["primary_access_key"].(string)
				Expect(ok).To(BeTrue(), "CRD should have a status.primary_access_key property")
				Expect(primaryAccessKey).ToNot(BeEmpty(), "status.primary_access_key should not be an empty string")
			}, 30)
			It("should have the status.network_rules property as an array", func() {
				var ok bool
				networkRules, ok := status["network_rules"].(interface{})
				Expect(ok).To(BeTrue(), "CRD should have a status.network_rules property")
				networkRule, ok := networkRules.(map[string]interface{})
				Expect(ok).To(BeTrue(), "status.network_rules should be a map[string]interface{}")
				Expect(networkRule["default_action"]).To(Equal("Deny"))
			}, 30)
			It("should encrypt sensitive values stored in the status property", func() {
				if !EncryptionKeyIsSet() {
					Skip("encryption key is not set, skipping test")
				}

				// After we encrypt data, we then base64 encode it before writing
				// it to the CRD. Therefore, we check whether the data is encoded
				// and not plaintext as a proxy for encryption being applied.
				// This requires the plaintext value to not be an already base64
				// encoded value such as an access key.
				var ok bool
				primaryBlobConnectionString, ok := status["primary_blob_connection_string"].(string)
				Expect(ok).To(BeTrue(), "CRD should have a primary_blob_connection_string property")
				Expect(len(primaryBlobConnectionString)).ToNot(Equal(0), "status.primary_blob_connection_string should not be empty")
				_, err := base64.StdEncoding.DecodeString(primaryBlobConnectionString)
				Expect(err).To(BeNil(), "status.primary_blob_connection_string should be base64 encoded")
				// TODO: Once refactored to use packages and the decryption function is exported
				// we can validate that this data is encrypted with the encryption key.
			}, 30)
			It("should have a tags property on the azure storage account resource in azure", func() {
				Expect(storageAccountResource.Tags).ToNot(BeNil())
				Expect(len(storageAccountResource.Tags)).To(Equal(1), "should have 1 tag")
				envrionmenTag, ok := storageAccountResource.Tags["environment"]
				Expect(ok).To(BeTrue(), "the tag key 'environment' should exist")
				Expect(*envrionmenTag).To(Equal("Production"))
			})
			It("should have a network acls property on the azure storage account resource in azure", func() {
				props, ok := storageAccountResource.Properties.(map[string]interface{})
				Expect(ok).To(BeTrue(), "properties should be of type map[string]interface{}")
				networkRules, ok := props["networkAcls"]
				Expect(ok).To(BeTrue(), "the networkAcls property should exist in the properties")
				networkRulesValues, ok := networkRules.(map[string]interface{})
				Expect(ok).To(BeTrue(), "the networkAcls property should be a map[string]interface{}")
				ipRules, ok := networkRulesValues["ipRules"]
				Expect(ok).To(BeTrue(), "the networkAcls property map should have an ip_rules key")
				ipRulesValues := ipRules.([]interface{})
				Expect(ok).To(BeTrue(), "the ipRules value should be []interface{}")
				Expect(len(ipRulesValues)).To(Equal(3))
			})
		})
		When("deleting an azure storage account", func() {
			It("should allow the CRD delete request", func() {
				err := k8sClientWithDefaults(gvrStorageAccount).Delete(context.TODO(), storageAccountName, metav1.DeleteOptions{})
				Expect(err).To(BeNil())
			})
			It("should eventually delete the azure storage account CRD", func() {
				Eventually(func() bool {
					_, err := k8sClientWithDefaults(gvrStorageAccount).Get(context.TODO(), storageAccountName, metav1.GetOptions{})
					if err != nil {
						if errors.IsNotFound(err) {
							return true
						}
						Expect(err).To(BeNil())
					}
					return false
				}, time.Minute*5, time.Second*10).Should(BeTrue())
			}, 300)
		})
		When("deleting an azure resource group", func() {
			It("should allow the CRD delete request", func() {
				err := k8sClientWithDefaults(gvrResourceGroup).Delete(context.TODO(), resourceGroupName, metav1.DeleteOptions{})
				Expect(err).To(BeNil())
			})
			It("should eventually delete the azure resource group CRD", func() {
				Eventually(func() bool {
					_, err := k8sClientWithDefaults(gvrResourceGroup).Get(context.TODO(), resourceGroupName, metav1.GetOptions{})
					if err != nil {
						if errors.IsNotFound(err) {
							return true
						}
						Expect(err).To(BeNil())
					}
					return false
				}, time.Minute*2, time.Second*10).Should(BeTrue())
			}, 300)
		})
	})
	Context("without respect to resource dependencies", func() {
		randomString := RandomString(12)
		resourceGroupName := "tftest-" + randomString
		resourceGroupLocation := "westeurope"
		storageAccountName := randomString
		storageAccountLocation := "westeurope"

		gvrResourceGroup := schema.GroupVersionResource{
			Group:    "azurerm.tfb.local",
			Version:  "valpha1",
			Resource: "resource-groups",
		}

		gvrStorageAccount := schema.GroupVersionResource{
			Group:    "azurerm.tfb.local",
			Version:  "valpha1",
			Resource: "storage-accounts",
		}
		When("creating an azure storage account before creating the azure resource group containing it", func() {
			azureStorageAccountCRDRequest := GetAzureStorageAccountCRD(resourceGroupName, storageAccountName, storageAccountLocation)
			var azureStorageAccountCRDResponse *unstructured.Unstructured
			azureResourecGroupCRDRequest := GetAzureResourceGroupCRD(resourceGroupName, resourceGroupLocation)
			var status map[string]interface{}
			var storageAccountID string
			It("should allow the azure storage account CRD to be created", func() {
				_, err := k8sClientWithDefaults(gvrStorageAccount).Create(context.TODO(), &azureStorageAccountCRDRequest, metav1.CreateOptions{})
				Expect(err).To(BeNil())

				// TODO - when we add events, wait for an event indicating retrying due to referenced resources
				// for now - wait a short time to allow the controller to attempt and then requeue
				time.Sleep(time.Second)
			}, 30)
			It("should allow the azure resource group CRD to be created", func() {
				_, err := k8sClientWithDefaults(gvrResourceGroup).Create(context.TODO(), &azureResourecGroupCRDRequest, metav1.CreateOptions{})
				Expect(err).To(BeNil())
			}, 30)
			It("should retry to create the azure storage account until the azure resource group is created", func() {
				Eventually(func() bool {
					var err error
					azureStorageAccountCRDResponse, err = k8sClientWithDefaults(gvrStorageAccount).Get(context.TODO(), storageAccountName, metav1.GetOptions{})
					Expect(err).To(BeNil())
					status, _ = azureStorageAccountCRDResponse.Object["status"].(map[string]interface{})
					var ok bool
					storageAccountID, ok = status["id"].(string)
					return ok
				}, time.Minute*3, time.Second*10).Should(BeTrue())
			}, 300)
			It("should have the status.id property set to a valid azure resource id", func() {
				Expect(storageAccountID).To(MatchRegexp("/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resourceGroups/" + resourceGroupName + "/providers/Microsoft.Storage/storageAccounts/" + storageAccountName))
			}, 30)
		})
		When("deleting the azure storage account", func() {
			It("should allow the CRD delete request", func() {
				err := k8sClientWithDefaults(gvrStorageAccount).Delete(context.TODO(), storageAccountName, metav1.DeleteOptions{})
				Expect(err).To(BeNil())
			})
			It("should delete the azure storage account CRD", func() {
				Eventually(func() bool {
					_, err := k8sClientWithDefaults(gvrStorageAccount).Get(context.TODO(), storageAccountName, metav1.GetOptions{})
					if err != nil {
						if errors.IsNotFound(err) {
							return true
						}
						Expect(err).To(BeNil())
					}
					return false
				}, time.Minute*5, time.Second*10).Should(BeTrue())
			}, 300)
		})
		When("deleting the azure resource group", func() {
			It("should allow the CRD delete request", func() {
				err := k8sClientWithDefaults(gvrResourceGroup).Delete(context.TODO(), resourceGroupName, metav1.DeleteOptions{})
				Expect(err).To(BeNil())
			})
			It("should delete the azure resource group CRD", func() {
				Eventually(func() bool {
					_, err := k8sClientWithDefaults(gvrResourceGroup).Get(context.TODO(), resourceGroupName, metav1.GetOptions{})
					if err != nil {
						if errors.IsNotFound(err) {
							return true
						}
						Expect(err).To(BeNil())
					}
					return false
				}, time.Second*120, time.Second*10).Should(BeTrue())
			}, 300)
		})
	})
})

func RandomString(n int) string {
	rand.Seed(time.Now().UnixNano())

	var letters = []rune("abcdefghijklmnopqrstuvwxyz")

	s := make([]rune, n)
	for i := range s {
		s[i] = letters[rand.Intn(len(letters))]
	}
	return string(s)
}

func EncryptionKeyIsSet() bool {
	if encryptionKey := os.Getenv(encryptionKeyEnvVar); encryptionKey == "" {
		return false
	}
	return true
}

func GetAzureResourceGroupCRD(name, location string) unstructured.Unstructured {
	return unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "azurerm.tfb.local/valpha1",
			"kind":       "resource-group",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"name":     name,
				"location": location,
			},
		},
	}
}

func GetAzureStorageAccountCRD(resourceGroup, name, location string) unstructured.Unstructured {
	return unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "azurerm.tfb.local/valpha1",
			"kind":       "storage-account",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"name":                     name,
				"resource_group_name":      resourceGroup,
				"location":                 location,
				"account_tier":             "Standard",
				"account_replication_type": "LRS",
				"network_rules": map[string]interface{}{
					"default_action": "Deny",
					"ip_rules": []string{
						"100.0.0.1",
						"100.0.0.2",
						"100.0.0.3",
					},
				},
				"static_website": map[string]interface{}{
					"index_document": "index.html",
				},
				"tags": map[string]string{
					"environment": "Production",
				},
			},
		},
	}
}

func k8sClientWithDefaults(res schema.GroupVersionResource) dynamic.ResourceInterface {
	return k8sClient.Resource(res).Namespace("default")
}

func GetAzureResourceGroup(ctx context.Context, resourceGroupName string) (resources.Group, error) {

	res, err := azureResourceGroupsClient.Get(
		ctx,
		resourceGroupName,
	)
	// Wrap the error to avoid sensitive details in build/logs
	if err != nil {
		return res, fmt.Errorf("failed getting resourcegroup: %s", err.Error())
	}

	return res, err
}

func GetAzureResource(ctx context.Context, resourceGroupName, resourceProvider, resourceType, resourceName, apiVersion string) (resources.GenericResource, error) {
	res, err := azureResourcesClient.Get(
		ctx,
		resourceGroupName,
		resourceProvider,
		"",
		resourceType,
		resourceName,
		apiVersion,
	)

	// Wrap the error to avoid sensitive details in build/logs
	if err != nil {
		return res, fmt.Errorf("failed getting resource: %s", err.Error())
	}

	return res, err
}
