package main_test

import (
	"context"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	main "github.com/lawrencegripper/tfoperatorbridge"
)

var _ = Describe("When working with a resource group", func() {
	name := "tftest-" + strings.ToLower(main.RandomString(12))
	gvrResourceGroup := schema.GroupVersionResource{
		Group:    "azurerm.tfb.local",
		Version:  "valpha1",
		Resource: "resource-groups",
	}

	It("should allow the resource lifecycle", func() {
		By("creating the resource-group CRD")

		Expect(k8sClient).ToNot(BeNil())

		obj := unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "azurerm.tfb.local/valpha1",
				"kind":       "resource-group",
				"metadata": map[string]interface{}{
					"name": name,
				},
				"spec": map[string]interface{}{
					"name":     name,
					"location": "westeurope",
				},
			},
		}
		options := metav1.CreateOptions{}
		_, err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Create(context.TODO(), &obj, options)
		Expect(err).To(BeNil())

		By("returning the resource ID")
		Eventually(func() bool {
			obj, err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Get(context.TODO(), name, metav1.GetOptions{})
			Expect(err).To(BeNil())

			status, ok := obj.Object["status"].(map[string]interface{})
			Expect(err).To(BeNil())
			if !ok {
				return false
			}

			id := status["id"].(string)
			if id != "" {
				return true
			}
			return false
		}, time.Second*10, time.Second*5).Should(BeTrue()) // TODO - use a regex match for /subscriptions/.../resourceGroups/<name>
		Expect(k8sClient).ToNot(BeNil())

		By("deleting the resource CRD")
		err = k8sClient.Resource(gvrResourceGroup).Namespace("default").Delete(context.TODO(), name, metav1.DeleteOptions{})
		Expect(err).To(BeNil())

		Eventually(func() bool {
			_, err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Get(context.TODO(), name, metav1.GetOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					return true
				}
				Expect(err).To(BeNil())
			}
			return false
		}, time.Second*120, time.Second*10).Should(BeTrue()) // TODO - use a regex match for /subscriptions/.../resourceGroups/<name>

	}, 20)

})
