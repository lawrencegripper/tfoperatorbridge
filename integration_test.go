package main_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

var _ = Describe("When creating CRDs sequentially after resources are created", func() {
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

	Context("When creating the resource group", func() {
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
			_, err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Create(context.TODO(), &objResourceGroup, metav1.CreateOptions{})
			Expect(err).To(BeNil())
		}, 30)
		It("should create the resource group and assign the status.id", func() {
			Eventually(func() string {
				obj, err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Get(context.TODO(), resourceGroupName, metav1.GetOptions{})
				Expect(err).To(BeNil())

				status, ok := obj.Object["status"].(map[string]interface{})
				Expect(err).To(BeNil())
				if !ok {
					return ""
				}

				id := status["id"].(string)
				return id
			}, time.Second*30, time.Second*5).Should(MatchRegexp("/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resourceGroups/" + resourceGroupName))
		}, 30)
		It("if encryption key set should have an encrypted terraform state", func() {
			if encryptionKey, exists := os.LookupEnv("TF_STATE_ENCRYPTION_KEY"); !exists || encryptionKey == "" {
				Skip("TF_STATE_ENCRYPTION_KEY not set!")
			}
			obj, err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Get(context.TODO(), resourceGroupName, metav1.GetOptions{})
			Expect(err).To(BeNil())

			tfStateString, gotTfState, err := unstructured.NestedString(obj.Object, "status", "_tfoperator", "tfState")
			Expect(err).To(BeNil())
			Expect(gotTfState).To(BeTrue(), "Terraform should have status._tfoperator.tfState property")

			var js string
			err = json.Unmarshal([]byte(tfStateString), &js)
			Expect(err).ToNot(BeNil())
		}, 30)
	})
	Context("When creating the storage account", func() {
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
						"resource_group_name":      resourceGroupName,
						"location":                 "westeurope",
						"account_tier":             "Standard",
						"account_replication_type": "LRS",
					},
				},
			}
			_, err := k8sClient.Resource(gvrStorageAccount).Namespace("default").Create(context.TODO(), &objStorageAccount, metav1.CreateOptions{})
			Expect(err).To(BeNil())
		}, 30)
		It("should create the storage account and assign the status", func() {
			By("returning the storage account properties in the status")
			Eventually(func() bool {
				obj, err := k8sClient.Resource(gvrStorageAccount).Namespace("default").Get(context.TODO(), storageAccountName, metav1.GetOptions{})
				Expect(err).To(BeNil())

				status, ok := obj.Object["status"].(map[string]interface{})
				Expect(err).To(BeNil())
				if !ok {
					return false
				}

				id := status["id"].(string)
				Expect(id).Should(MatchRegexp("/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resourceGroups/" + resourceGroupName))
				name := status["name"].(string)
				Expect(name).Should(Not(BeEmpty()))
				primaryAccessKey := status["primary_access_key"].(string)
				Expect(primaryAccessKey).Should(Not(BeEmpty()))

				return true
			}, time.Minute*3, time.Second*5).Should(BeTrue())
		}, 300)
		It("should create the storage account and assign the status with sensitive values", func() {
			By("returning the storage account primary_access_key encrypted")
			Eventually(func() error {
				if encryptionKey, exists := os.LookupEnv("TF_STATE_ENCRYPTION_KEY"); !exists || encryptionKey == "" {
					Skip("TF_STATE_ENCRYPTION_KEY not set!")
				}

				obj, err := k8sClient.Resource(gvrStorageAccount).Namespace("default").Get(context.TODO(), storageAccountName, metav1.GetOptions{})
				Expect(err).To(BeNil())

				status, ok := obj.Object["status"].(map[string]interface{})
				Expect(err).To(BeNil())
				if !ok {
					return fmt.Errorf("status not found in object map")
				}

				// If encoded, assume encrypted
				primaryAccessKey := status["primary_access_key"].(string)
				_, err = base64.StdEncoding.DecodeString(primaryAccessKey)
				return err
			}, time.Minute*3, time.Second*5).Should(BeNil())
		}, 300)
	})
	Context("When deleting the storage account", func() {
		It("should allow the CRD delete request", func() {
			err := k8sClient.Resource(gvrStorageAccount).Namespace("default").Delete(context.TODO(), storageAccountName, metav1.DeleteOptions{})
			Expect(err).To(BeNil())
		})
		It("should delete CRD", func() {
			Eventually(func() bool {
				_, err := k8sClient.Resource(gvrStorageAccount).Namespace("default").Get(context.TODO(), storageAccountName, metav1.GetOptions{})
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
			err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Delete(context.TODO(), resourceGroupName, metav1.DeleteOptions{})
			Expect(err).To(BeNil())
		})
		It("should delete CRD", func() {
			Eventually(func() bool {
				_, err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Get(context.TODO(), resourceGroupName, metav1.GetOptions{})
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

var _ = Describe("When creating CRDs out of order with references", func() {
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
			_, err := k8sClient.Resource(gvrStorageAccount).Namespace("default").Create(context.TODO(), &objStorageAccount, metav1.CreateOptions{})
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
			_, err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Create(context.TODO(), &objResourceGroup, metav1.CreateOptions{})
			Expect(err).To(BeNil())
		}, 30)
	})
	It("should retry and create the storage account and assign the status.id", func() {
		By("returning the storage account ID")
		Eventually(func() string {
			obj, err := k8sClient.Resource(gvrStorageAccount).Namespace("default").Get(context.TODO(), storageAccountName, metav1.GetOptions{})
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
			err := k8sClient.Resource(gvrStorageAccount).Namespace("default").Delete(context.TODO(), storageAccountName, metav1.DeleteOptions{})
			Expect(err).To(BeNil())
		})
		It("should delete CRD", func() {
			Eventually(func() bool {
				_, err := k8sClient.Resource(gvrStorageAccount).Namespace("default").Get(context.TODO(), storageAccountName, metav1.GetOptions{})
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
			err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Delete(context.TODO(), resourceGroupName, metav1.DeleteOptions{})
			Expect(err).To(BeNil())
		})
		It("should delete CRD", func() {
			Eventually(func() bool {
				_, err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Get(context.TODO(), resourceGroupName, metav1.GetOptions{})
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
