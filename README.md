# Terraform Operator Bridge - Create K8s Operators from any Terraform Provider

Terraform Providers have been built out for lots of APIs such as Cloud providers or solutions. 

The providers offer: 
- Resource definitions with Schemas and validation
- Deterministic CRUD methods over resources
- State Tracking for tracking drift

Essentially these are all the things required to build an Operator in Kubernetes to manage a resource. 

This projects aims to allow any Terraform Provider to be wrapped into a K8s Operator. 

# Aims

Users should be able to deploy the bridge into their cluster with a provider selected like "AzureRMv1.2". It should then:

1. Generation of mappings TF->CRD should take place at startup/in cluster for ease of use. (Build time step considered but discarded due to added complexity on the user)
1. Add the CRD definitions into the clusters api server
2. Validate CRDs submitted using the TF Schema and Validation functions
3. Detect drift from desired state and correct


# Notes

- OpenAPI Generation in K8s https://github.com/kubernetes/kube-openapi/blob/master/pkg/generators/openapi.go
- Schema Types in TF https://www.terraform.io/docs/extend/schemas/schema-types.html
- Generate a openapi spec like this https://github.com/microsoft/azure-databricks-operator/blob/master/config/crd/bases/databricks.microsoft.com_dbfsblocks.yaml from the TF spec

