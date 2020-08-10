package tfprovider

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	. "github.com/onsi/ginkgo"

	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("Testing with Ginkgo", func() {
	It("setup provider", func() {

		tests := []struct {
			purpose string
			envs    map[string]string
			wantErr bool
		}{
			{
				purpose: "Error_When_OnlyNameSet",
				envs: map[string]string{
					"TF_PROVIDER_NAME": "azurerm",
				},
				wantErr: true,
			},
			{
				purpose: "Error_When_PathIsMissingProviderBinary",
				envs: map[string]string{
					"TF_PROVIDER_NAME": "azurerm",
					"TF_PROVIDER_PATH": "/tmp",
				},
				wantErr: true,
			},
			{
				purpose: "Succeed_When_ValidProviderPathAndNameSet",
				envs: map[string]string{
					"TF_PROVIDER_NAME": "azurerm",
					"TF_PROVIDER_PATH": providerInstallPath,
				},
				wantErr: false,
			},
			{
				purpose: "Succeed_When_ValidProviderNameAndVersionSet",
				envs: map[string]string{
					"TF_PROVIDER_NAME":    "azurerm",
					"TF_PROVIDER_VERSION": "2.22.0",
				},
				wantErr: false,
			},
		}
		for _, tt := range tests {
			When(tt.purpose, func() {
				for name, value := range tt.envs {
					err := os.Setenv(name, value)
					if err != nil {
						GinkgoT().Errorf("failed to set environment vars for test. error = %v", err)
					}
				}
				got, err := SetupProvider(ctrl.Log.WithName("main"))
				if (err != nil) != tt.wantErr {
					GinkgoT().Errorf("SetupProvider() error = %v, wantErr %v", err, tt.wantErr)
					return
				}

				if err != nil && tt.wantErr == false {

					schemaResult := got.GetSchema()
					if len(schemaResult.Diagnostics) > 0 {
						GinkgoT().Errorf("failed to get schema from provider")
					}
				}
			})
		}
	})
})

const (
	providerInstallPath = "./hack/.terraform/plugins/linux_amd64/"
)

type providerTestDef struct {
	name         string
	requiredEnvs map[string]string
	configBody   string
	version      string
}

var testedProviders = []providerTestDef{
	{
		name:         "aws",
		requiredEnvs: map[string]string{"AWS_REGION": "us-east-1"},
		version:      "2.70.0",
	},
	{
		name:         "azurerm",
		requiredEnvs: map[string]string{},
		configBody:   `features {}`,
		version:      "2.22.0",
	},
	{
		name:         "helm",
		requiredEnvs: map[string]string{},
		version:      "1.2.3",
	},
}

func Test_PrepareProviderConfigWithDefaults_expectNoError(t GinkgoTInterface) {
	for _, tt := range testedProviders {
		When(tt.name, func() {

			err := os.Setenv("PROVIDER_CONFIG_HCL", "")
			if err != nil {
				panic(err)
			}

			for name, value := range tt.requiredEnvs {
				err = os.Setenv(name, value)
				if err != nil {
					panic(err)
				}
			}
			provider, err := getInstanceOfProvider(tt.name, providerInstallPath)
			if err != nil {
				t.Errorf("failed to get instance of provider. error = %v", err)
			}

			_, err = createEmptyProviderConfWithDefaults(provider, tt.configBody)
			if err != nil {
				t.Errorf("failed to configure provider with defaults. error = %v", err)
				return
			}
		})
	}
}

func Test_installProvider(t GinkgoTInterface) {
	for _, tt := range testedProviders {
		When(tt.name, func() {
			installedToPath, err := installProvider(tt.name, tt.version)
			if err != nil {
				t.Errorf("installProvider() error = %v, no error expected", err)
			}

			expectedProviderFilename := fmt.Sprintf("terraform-provider-%s_v%s", tt.name, tt.version)

			files, err := ioutil.ReadDir(installedToPath)
			if err != nil {
				t.Errorf("failed to read dir. error = %v", err)
			}

			foundProvider := false
			for _, file := range files {
				if strings.HasPrefix(file.Name(), expectedProviderFilename) {

					foundProvider = true
				}
			}

			if !foundProvider {
				t.Errorf("missing provider for %q in %q", tt.name, installedToPath)
			}
		})
	}
}
