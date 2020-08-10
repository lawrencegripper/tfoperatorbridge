# Terraform Operator Bridge - Create K8s Operators from any Terraform Provider

![build](https://github.com/lawrencegripper/tfoperatorbridge/workflows/build/badge.svg)

Terraform Providers have been built out for lots of APIs such as Cloud providers or solutions. 

The providers offer: 
- Resource definitions with Schemas and validation
- Deterministic CRUD methods over resources
- State Tracking for tracking drift

Essentially these are all the things required to build an Operator in Kubernetes to manage a resource. 

This projects aims to allow any Terraform Provider to be wrapped into a K8s Operator. 

## Status 

Currently this looks to test whether the required basic behaviour is possible. It is not functional.

## Aims

Users should be able to deploy the bridge into their cluster with a provider selected like `AzureRMv1.2`. It should then:

1. Generate mapping of TF->CRD. This should take place at startup/in cluster for ease of use. (Build time step considered but discarded due to added complexity on the user)
1. Add the CRD definitions into the clusters api server
2. Validate CRDs submitted using the TF Schema and Validation functions
3. Detect drift from desired state and correct


## Testing

1. Copy `.env-template` to `.env` and populate the fields with your SP/Subscription details
1. `make run`

> Warning: **Do not wrap values in `"`'s** in the `.env` file. These are imported by `Make` and adding quotes causes this to break. For example `TF_PROVIDER_PATH=./hack/.terraform/plugins/linux_amd64/` is correct.

## Config 

- `ENABLE_PROVIDER_LOG` - Enable full provider logs
- `SKIP_CRD_CREATION`   - Skip CRD creation at startup

You can configure which terraform provider is used by the operator in two ways. The provider is automatically downloaded if `TF_PROVIDER_NAME` and `TF_PROVIDER_VERSION` are set or is pulled from an existing location on disk if `TF_PROVIDER_NAME` and `TF_PROVIDER_PATH` are set.

- `TF_PROVIDER_NAME`    - Terraform provider you'd like to use. eg `azurerm`
- `TF_PROVIDER_VERSION` - Version of the terraform provider. eg `2.20.0`
- `TF_PROVIDER_PATH`    - Path on disk to the binary of the provider. eg "/providers/here". This folder must contain a provider binary in the format `terraform-provider-azurerm_v2.22`

## Provider Config

There are two ways to configure the provider used by the operator. 

1. Normal provider environment variables
2. An environment variable which contains the HCL for provider config

They can be used individually or together.

For an example of how these can be used together [see the `azurerm` `.env` file](./.env-template-azurerm).

### Environment variables

The providers will pickup and use the same environment variables as when used in normal terraform. 

For example, the following will set `client_id` in the `azurerm` provider. 

`ARM_CLIENT_ID=<YOUR-CLIENT-HERE>`

### HCL Configuration

There is a special environment variable which can be used for more complex provider configuration. This is a fallback option.

`PROVIDER_CONFIG_HCL` Lets you define the config for the provider as you would in a terraform block. For example:

```hcl
provider "azurerm" {
  features {}
}
```
Can be set as follows:
```
PROVIDER_CONFIG_HCL="features {}"
```





## Notes

- OpenAPI Generation in K8s https://github.com/kubernetes/kube-openapi/blob/master/pkg/generators/openapi.go
- Schema Types in TF https://www.terraform.io/docs/extend/schemas/schema-types.html
