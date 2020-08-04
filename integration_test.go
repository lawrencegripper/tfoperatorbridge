package main_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"math/rand"
	"os"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

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
	if encryptionKey, exists := os.LookupEnv("TF_STATE_ENCRYPTION_KEY"); !exists || encryptionKey == "" {
		return false
	}
	return true
}

func GetAzureResourceGroup(name, location string) unstructured.Unstructured {
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

func GetAzureStorageAccount(resourceGroup, name, location string) unstructured.Unstructured {
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
				"network_rules": []map[string]interface{}{
					{
						"default_action": "Deny",
						"ip_rules": []string{
							"100.0.0.1",
						},
					},
					{
						"default_action": "Allow",
						"ip_rules": []string{
							"100.0.0.2",
						},
					},
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

var _ = Describe("Azure resource creation in order", func() {
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
		azureResourecGroupCRDRequest := GetAzureResourceGroup(resourceGroupName, resourceGroupLocation)
		var azureResourceGroupCRDResponse *unstructured.Unstructured
		var resourceGroupId string
		It("should create without error", func() {
			_, err := k8sClientWithDefaults(gvrResourceGroup).Create(context.TODO(), &azureResourecGroupCRDRequest, metav1.CreateOptions{})
			Expect(err).To(BeNil())
		}, 30)
		It("should have a status.id property set", func() {
			Eventually(func() bool {
				var err error
				azureResourceGroupCRDResponse, err = k8sClientWithDefaults(gvrResourceGroup).Get(context.TODO(), resourceGroupName, metav1.GetOptions{})
				Expect(err).To(BeNil())
				status, _ := azureResourceGroupCRDResponse.Object["status"].(map[string]interface{})
				var ok bool
				resourceGroupId, ok = status["id"].(string)
				return ok
			}, time.Second*45, time.Second*5).Should(BeTrue(), "CRD should have status.id property")
		}, 30)
		It("should have a status.id property set to a valid azure resource id", func() {
			Expect(resourceGroupId).To(MatchRegexp("/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resourceGroups/" + resourceGroupName))
		}, 30)
		It("should store terraform state encrypted in status._tfoperator.tfState", func() {
			if EncryptionKeyIsSet() {
				Skip("encryption key is not set, skipping test")
			}
			tfStateString, gotTfState, err := unstructured.NestedString(azureResourceGroupCRDResponse.Object, "status", "_tfoperator", "tfState")
			Expect(err).To(BeNil())
			Expect(gotTfState).To(BeTrue(), "CRD should have status._tfoperator.tfState property")

			var js string
			err = json.Unmarshal([]byte(tfStateString), &js)
			Expect(err).ToNot(BeNil()) // Check state is encrypted as it's not valid JSON
		}, 30)
	})
	When("creating the azure storage account CRD", func() {
		azureStorageAccountCRDRequest := GetAzureStorageAccount(resourceGroupName, storageAccountName, storageAccountLocation)
		var azureStorageAccountCRDResponse *unstructured.Unstructured
		var status map[string]interface{}
		var storageAccountId string
		It("should create without error", func() {
			_, err := k8sClientWithDefaults(gvrStorageAccount).Create(context.TODO(), &azureStorageAccountCRDRequest, metav1.CreateOptions{})
			Expect(err).To(BeNil())
		}, 30)
		It("should have the status.id property set", func() {
			Eventually(func() bool {
				var err error
				azureStorageAccountCRDResponse, err = k8sClientWithDefaults(gvrStorageAccount).Get(context.TODO(), storageAccountName, metav1.GetOptions{})
				Expect(err).To(BeNil())
				status, _ = azureStorageAccountCRDResponse.Object["status"].(map[string]interface{})
				var ok bool
				storageAccountId, ok = status["id"].(string)
				return ok
			}, time.Second*45, time.Second*5).Should(BeTrue())
		}, 30)
		It("should have the status.id property set to a valid azure resource id", func() {
			Expect(storageAccountId).To(MatchRegexp("/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resourceGroups/" + resourceGroupName + "/providers/Microsoft.Storage/storageAccounts/" + storageAccountName))
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
		It("should have the status.tags property as an object", func() {
			var ok bool
			tags, ok := status["tags"].(interface{})
			Expect(ok).To(BeTrue(), "CRD should have a status.tags property")
			tagMap, ok := tags.(map[string]interface{})
			Expect(ok).To(BeTrue(), "status.tags property should be a map[string]string")
			log.Printf("TAGS: %+v", tagMap)
			val, ok := tagMap["environment"]
			Expect(ok).To(BeTrue(), "status.tags property map should have key environment")
			environment, ok := val.(string)
			Expect(ok).To(BeTrue(), "status.tags property map key environment should have a string value")
			Expect(environment).To(Equal("Production"))
		}, 30)
		It("should have the status.network_rules property as an array", func() {
			var ok bool
			networkRules, ok := status["network_rules"].([]interface{})
			Expect(ok).To(BeTrue(), "CRD should have a status.network_rules property")
			Expect(len(networkRules)).To(Equal(2))
			networkRule1 := networkRules[0].(map[string]interface{})
			Expect(networkRule1["default_action"]).To(Equal("Deny"))
			networkRule2 := networkRules[1].(map[string]interface{})
			Expect(networkRule2["default_action"]).To(Equal("Allow"))
		}, 30)
		It("should encrypt sensitive values stored in the status property", func() {
			if EncryptionKeyIsSet() {
				Skip("encryption key is not set, skipping test")
			}

			// If base64 encoded, assume we have encrypted the value as well
			var ok bool
			primaryAccessKey, ok := status["primary_access_key"].(string)
			Expect(ok).To(BeTrue(), "CRD should have a primary_access_key property")
			var err error
			_, err = base64.StdEncoding.DecodeString(primaryAccessKey)
			Expect(err).To(BeNil(), "status.primary_access_key should be base64 encoded and encrypted")
		}, 30)
	})
	When("deleting the azure storage account", func() {
		It("should allow the CRD delete request", func() {
			err := k8sClientWithDefaults(gvrStorageAccount).Delete(context.TODO(), storageAccountName, metav1.DeleteOptions{})
			Expect(err).To(BeNil())
		})
		It("should delete the CRD", func() {
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
		It("should delete the CRD", func() {
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

var _ = Describe("Azure resource creation out of order", func() {
	// This test creates the storage account CRD first but the referenced resource group doesn't exist
	// It checks that it retries and succeeds once the resource group is created

	randomString := RandomString(12)
	resourceGroupName := "tftest-" + randomString
	storageAccountName := randomString

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

	Context("When creating the CRDs", func() {
		It("should allow the storage-account CRD to be created", func() {
			objStorageAccount := unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "azurerm.tfb.local/valpha1",
					"kind":       "storage-account",
					"metadata": map[string]interface{}{
						"name": storageAccountName,
					},
					"spec": map[string]interface{}{
						"name":                     storageAccountName,
						"resource_group_name":      "`azurerm.tfb.local:valpha1:resource-group:default:" + resourceGroupName + ":spec.name",
						"location":                 "westeurope",
						"account_tier":             "Standard",
						"account_replication_type": "LRS",
					},
				},
			}
			_, err := k8sClientWithDefaults(gvrStorageAccount).Create(context.TODO(), &objStorageAccount, metav1.CreateOptions{})
			Expect(err).To(BeNil())

			// TODO - when we add events, wait for an event indicating retrying due to referenced resources
			// for now - wait a short time to allow the controller to attempt and then requeue
			time.Sleep(time.Second)
		}, 30)
		It("should allow the resource-group CRD to be created", func() {

			Expect(k8sClient).ToNot(BeNil())

			objResourceGroup := unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "azurerm.tfb.local/valpha1",
					"kind":       "resource-group",
					"metadata": map[string]interface{}{
						"name": resourceGroupName,
					},
					"spec": map[string]interface{}{
						"name":     resourceGroupName,
						"location": "westeurope",
					},
				},
			}
			_, err := k8sClientWithDefaults(gvrResourceGroup).Create(context.TODO(), &objResourceGroup, metav1.CreateOptions{})
			Expect(err).To(BeNil())
		}, 30)
	})
	It("should retry and create the storage account and assign the status.id", func() {
		By("returning the storage account ID")
		Eventually(func() string {
			obj, err := k8sClientWithDefaults(gvrStorageAccount).Get(context.TODO(), storageAccountName, metav1.GetOptions{})
			Expect(err).To(BeNil())

			status, ok := obj.Object["status"].(map[string]interface{})
			Expect(err).To(BeNil())
			if !ok {
				return ""
			}

			id := status["id"].(string)
			return id
		}, time.Minute*3, time.Second*5).Should(Not(BeEmpty())) // TODO check id format
	}, 300)
	Context("When deleting the storage account", func() {
		It("should allow the CRD delete request", func() {
			err := k8sClientWithDefaults(gvrStorageAccount).Delete(context.TODO(), storageAccountName, metav1.DeleteOptions{})
			Expect(err).To(BeNil())
		})
		It("should delete CRD", func() {
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
	Context("When deleting the storage account", func() {
		It("should allow the CRD delete request", func() {
			err := k8sClientWithDefaults(gvrResourceGroup).Delete(context.TODO(), resourceGroupName, metav1.DeleteOptions{})
			Expect(err).To(BeNil())
		})
		It("should delete CRD", func() {
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
