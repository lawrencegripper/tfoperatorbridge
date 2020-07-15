package main_test

import (
	"context"
	"math/rand"
	"strings"
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

	var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

	s := make([]rune, n)
	for i := range s {
		s[i] = letters[rand.Intn(len(letters))]
	}
	return string(s)
}

var _ = Describe("When working with a resource group", func() {
	name := "tftest-" + strings.ToLower(RandomString(12))
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
		Eventually(func() string {
			obj, err := k8sClient.Resource(gvrResourceGroup).Namespace("default").Get(context.TODO(), name, metav1.GetOptions{})
			Expect(err).To(BeNil())

			status, ok := obj.Object["status"].(map[string]interface{})
			Expect(err).To(BeNil())
			if !ok {
				return ""
			}

			id := status["id"].(string)
			return id
		}, time.Second*10, time.Second*5).Should(MatchRegexp("/subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resourceGroups/" + name))

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
