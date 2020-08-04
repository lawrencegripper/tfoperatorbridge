package main_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

var k8sClient dynamic.Interface

func TestTfOperatorBridge(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Workspace Suite")
}

var _ = BeforeSuite(func() {
	var kubeconfig string
	if home := os.Getenv("HOME"); home != "" {
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).To(BeNil())

	k8sClient, err = dynamic.NewForConfig(config)
	Expect(err).To(BeNil())
	Expect(k8sClient).ToNot(BeNil())
}, 60)
