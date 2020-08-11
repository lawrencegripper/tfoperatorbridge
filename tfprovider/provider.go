package tfprovider

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path"

	"github.com/go-logr/logr"
	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/hashicorp/terraform-exec/tfinstall"
	"github.com/hashicorp/terraform/lang"
	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/plugin/discovery"
	"github.com/hashicorp/terraform/providers"

	"github.com/zclconf/go-cty/cty"
)

const (
	providerNameEnv   = "TF_PROVIDER_NAME"
	providerVerionEnv = "TF_PROVIDER_VERSION"
	providerPathEnv   = "TF_PROVIDER_PATH"
	tfVersion         = "0.12.29"
)

// SetupProvider will return an instance of the TF Provider.
// It will download [optional] and configure the provider before returning it.
func SetupProvider(log logr.Logger) (*plugin.GRPCProvider, error) {
	providerName := os.Getenv(providerNameEnv)
	if providerName == "" {
		return nil, fmt.Errorf("Env %q not set and is required", providerNameEnv)
	}
	pathFromEnv := os.Getenv(providerPathEnv)

	var providerInstance *plugin.GRPCProvider
	var err error

	// Best route for serious use cases is to setup the provider binary as a volume mounted
	// into the container.
	if pathFromEnv != "" {
		log.Info("Getting provider instance using path")
		providerInstance, err = getInstanceOfProvider(providerName, pathFromEnv)
		if err != nil {
			return nil, fmt.Errorf("failed getting provider instance %w", err)
		}
	} else {
		// If only the provider name and version are provided we'll install TF and use
		// `terraform init` to install the provider from hashicorp registry
		log.Info("Downloading provider binary")
		versionFromEnv := os.Getenv(providerVerionEnv)
		if versionFromEnv == "" {
			return nil, fmt.Errorf("Env %q not set and is required when path to provider binary isn't set with %q", providerVerionEnv, providerPathEnv)
		}
		path, err := installProvider(providerName, versionFromEnv)
		if err != nil {
			return nil, fmt.Errorf("Failed to setup provider as provider install failed: %w", err)
		}

		providerInstance, err = getInstanceOfProvider(providerName, path)
		if err != nil {
			return nil, fmt.Errorf("failed getting provider instance %w", err)
		}
	}

	return configureProvider(log, providerInstance)
}

func installProvider(name string, version string) (string, error) {
	tmpDir, err := ioutil.TempDir("", "tfinstall")
	if err != nil {
		return "", fmt.Errorf("Failed to create temp dir. %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint: errcheck

	execPath, err := tfinstall.Find(tfinstall.ExactVersion(tfVersion, tmpDir))
	if err != nil {
		return "", fmt.Errorf("Failed to install Terraform %w", err)
	}

	workingDir, err := ioutil.TempDir("", "tfproviders")
	if err != nil {
		return "", fmt.Errorf("Failed create tfprovider dir %w", err)
	}

	providerFileContent := fmt.Sprintf(`
	provider "%s" {
		version = "%s"
	}
	`, name, version)

	err = ioutil.WriteFile(path.Join(workingDir, "provider.tf"), []byte(providerFileContent), 0644)
	if err != nil {
		return "", fmt.Errorf("Failed to create provider.tf file %w", err)
	}
	tf, err := tfexec.NewTerraform(workingDir, execPath)
	if err != nil {
		return "", fmt.Errorf("Failed to create TF context %w", err)
	}

	err = tf.Init(context.Background(), tfexec.Upgrade(true), tfexec.LockTimeout("60s"))
	if err != nil {
		return "", fmt.Errorf("Failed to init TF %w", err)
	}

	return path.Join(workingDir, "/.terraform/plugins/linux_amd64/"), nil
}

func getInstanceOfProvider(providerName string, path string) (*plugin.GRPCProvider, error) {
	pluginMeta := discovery.FindPlugins(plugin.ProviderPluginName, []string{path}).WithName(providerName)

	if pluginMeta.Count() < 1 {
		return nil, fmt.Errorf("Provide:%q not found at path:%q", providerName, path)
	}
	clientConfig := plugin.ClientConfig(pluginMeta.Newest())

	// Don't log provider details unless provider log is enabled by env
	if _, exists := os.LookupEnv("ENABLE_PROVIDER_LOG"); !exists {
		clientConfig.Logger = hclog.NewNullLogger()
	}
	pluginClient := goplugin.NewClient(clientConfig)

	rpcClient, err := pluginClient.Client()

	if err != nil {
		return nil, fmt.Errorf("Failed to initialize plugin: %w", err)
	}
	// create a new resource provisioner.
	raw, err := rpcClient.Dispense(plugin.ProviderPluginName)
	if err != nil {
		panic(fmt.Errorf("Failed to dispense plugin: %s", err))
	}
	return raw.(*plugin.GRPCProvider), nil
}

func createEmptyProviderConfWithDefaults(provider *plugin.GRPCProvider, configBody string) (*cty.Value, error) {
	if configBody == "" {
		configBody = os.Getenv("PROVIDER_CONFIG_HCL")
	}

	providerConfigBlock := provider.GetSchema().Provider.Block

	// Parse the content of the provider block given to us into a body.
	// Note: The file name is required but isn't important in this context so we provide a nonexistent dummy filename.
	file, diagParse := hclsyntax.ParseConfig([]byte(configBody), "dummy.tf", hcl.Pos{})
	if diagParse.HasErrors() {
		return nil, fmt.Errorf("Failed parsing provider config block: %s", diagParse.Error())
	}

	scope := lang.Scope{}
	expandedConf, diags := scope.ExpandBlock(file.Body, providerConfigBlock)
	if diags.Err() != nil {
		return nil, fmt.Errorf("Failed expanding provider config block: %w", diags.Err())
	}
	configFull, diags := scope.EvalBlock(expandedConf, providerConfigBlock)
	if diags.Err() != nil {
		return nil, fmt.Errorf("Failed evaluating provider config block: %w", diags.Err())
	}

	// Call the `PrepareProviderConfig` with the config object. This returns a version of that config with the
	// required default setup as `PreparedConfig` under the response object.
	// Warning: Diagnostics houses errors, the typical go err pattern isn't followed - must check `resp.Diagnostics.Err()`
	prepConfigResp := provider.PrepareProviderConfig(providers.PrepareProviderConfigRequest{
		Config: configFull,
	})
	if err := prepConfigResp.Diagnostics.Err(); err != nil {
		return nil, fmt.Errorf(`Failed to set configure provider from config: %w.`+
			`Hint: See startup docs on using "PROVIDER_CONFIG_HCL" or the providers env vars to set required fields`, err)
	}

	return &configFull, nil
}

func configureProvider(log logr.Logger, provider *plugin.GRPCProvider) (*plugin.GRPCProvider, error) {
	configWithDefaults, err := createEmptyProviderConfWithDefaults(provider, "")
	if err != nil {
		return nil, err
	}
	// Now we have a prepared config we can configure the provider.
	// Warning (again): Diagnostics houses errors, the typical go err pattern isn't followed - must check `resp.Diagnostics.Err()`
	configureProviderResp := provider.Configure(providers.ConfigureRequest{
		Config: *configWithDefaults,
	})
	if err := configureProviderResp.Diagnostics.Err(); err != nil {
		log.Error(err, fmt.Sprintf("Failed to configure provider: %s", err))
		return nil, err
	}

	return provider, nil
}
