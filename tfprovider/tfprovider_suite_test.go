package tfprovider_test

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestTfprovider(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Tfprovider Suite")
}
