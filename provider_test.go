package main

import (
	"testing"
)

// Ensure you run ./hack/init.sh to
// install the providers before running these tests
var testedProviders = []string{
	"azurerm",
	"azuread",
	"helm",
	// "aws", // Not working atm
}

func Test_PrepareProviderConfigWithDefaults_expectNoError(t *testing.T) {

	for _, providerName := range testedProviders {
		t.Run(providerName, func(t *testing.T) {

			provider := getInstanceOfProvider(providerName)

			_, err := createEmptyProviderConfWithDefaults(provider)
			if err != nil {
				t.Errorf("failed to configure provider with defaults. error = %v", err)
				return
			}
		})
	}
}
