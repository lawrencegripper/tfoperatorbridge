package main_test

import (
	"context"
	"math/rand"
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
			}, time.Second*10, time.Second*5).Should(MatchRegexp("/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resourceGroups/" + resourceGroupName))
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
		It("should create the storage account and assign the status.id", func() {

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
