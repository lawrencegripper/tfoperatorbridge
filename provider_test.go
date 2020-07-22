package main

import (
	"testing"
)

func Test_ConfigureProvidersWithDefaults_expectNoError(t *testing.T) {

	tests := []string{
		"azurerm", "azuread", "aws", "helm",
	}
	for _, providerName := range tests {
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
