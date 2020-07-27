package main

import (
	"os"
	"testing"
)

type providerTestDef struct {
	name         string
	requiredEnvs map[string]string
	configBody   string
}

// Ensure you run ./hack/init.sh to
// install the providers before running these tests
var testedProviders = []providerTestDef{
	{
		name: "aws",
		requiredEnvs: map[string]string{
			"AWS_REGION": "us-east-1",
		},
	},
	{
		name:         "azurerm",
		requiredEnvs: map[string]string{},
		configBody:   `features {}`,
	},
	{
		name:         "helm",
		requiredEnvs: map[string]string{},
	},
}

func Test_PrepareProviderConfigWithDefaults_expectNoError(t *testing.T) {
	for _, tt := range testedProviders {
		t.Run(tt.name, func(t *testing.T) {
			// Clear the HCL config env if already set by previous test
			os.Setenv("PROVIDER_CONFIG_HCL", "")

			// Set required envs
			for name, value := range tt.requiredEnvs {
				os.Setenv(name, value)
			}
			provider := getInstanceOfProvider(tt.name)

			_, err := createEmptyProviderConfWithDefaults(provider, tt.configBody)
			if err != nil {
				t.Errorf("failed to configure provider with defaults. error = %v", err)
				return
			}
		})
	}
}
