package main_test

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	main "github.com/lawrencegripper/tfoperatorbridge"
)

var _ = Describe("When listing pods", func() {
	name := "tftest-" + strings.ToLower(main.RandomString(12))
	gvrResourceGroup := schema.GroupVersionResource{
		Group:    "azurerm.tfb.local",
		Version:  "valpha1",
		Resource: "resource-groups",
	}

	It("should succeed in creating the resource-group CRD", func() {
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
	}, 20)

	It("should succeed in deleting the resource-group CRD", func() {
		Expect(k8sClient).ToNot(BeNil())

		options := metav1.DeleteOptions{}
		err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Delete(context.TODO(), name, options)
		Expect(err).To(BeNil())
	}, 20)
})
