# Terraform Operator Bridge - Create K8s Operators from any Terraform Provider

Terraform Providers have been built out for lots of APIs such as Cloud providers or solutions. 

The providers offer: 
- Resource definitions with Schemas and validation
- Deterministic CRUD methods over resources
- State Tracking for tracking drift

Essentially these are all the things required to build an Operator in Kubernetes to manage a resource. 

This projects aims to allow any Terraform Provider to be wrapped into a K8s Operator. 

# Aims

Users should be able to start the bridge with a provider selected and deploy it into their cluster. It should then:

1. Generation of mappings TF->CRD should take place at startup/in cluster for ease of use. (Build time step considered but discarded due to added complexity on the user)
2. Validate CRDs submitted using the TF Schema and Validation functions
3. Detect drift from desired state and correct

