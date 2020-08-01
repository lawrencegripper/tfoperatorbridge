package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	openapi_spec "github.com/go-openapi/spec"
	"github.com/hashicorp/terraform/configs/configschema"
	terraform_schema "github.com/hashicorp/terraform/configs/configschema"
	terraform_plugin "github.com/hashicorp/terraform/plugin"
	"github.com/zclconf/go-cty/cty"

	k8s_apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	k8s_apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	k8s_apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8s_metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s_schema "k8s.io/apimachinery/pkg/runtime/schema"
	k8s_wait "k8s.io/apimachinery/pkg/util/wait"
)

func createCRDsForResources(provider *terraform_plugin.GRPCProvider) []GroupVersionFull {
	// Status: This runs but very little validation of the outputted openAPI apecs has been done. Bugs are likely

	tfSchema := provider.GetSchema()

	// Each resource in terraform becomes a openAPI Schema stored in this array
	resources := []openapi_spec.Schema{}

	// Foreach of the resources create a openAPI spec.
	// 1. Split terraform computed values into `status` of the CRD as these are unsettable by user
	// 2. Put required and optional params into the `spec` of the CRD. Setting required status accordinly.
	for resourceName, resource := range tfSchema.ResourceTypes {
		// Skip any resources which aren't valid DNS names as they're too long
		if len(resourceName) > 63 {
			fmt.Printf("Skipping invalid resource - name too long %q", resourceName)
			continue
		}

		// Create objects for both the spec and status blocks
		specCRD := openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type:     openapi_spec.StringOrArray{"object"},
				Required: []string{},
			},
		}
		// statusCRD is ONLY set for documentation purposes and is not currently enforced in reconciliation
		statusCRD := openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type:     openapi_spec.StringOrArray{"object"},
				Required: []string{},
			},
		}

		// Given the schema block for the tf resources add the properties to
		// either spec or status objects created above
		addBlockToSchema(&statusCRD, &specCRD, resource.Block)

		// Add tf operator property
		statusCRD.Properties["_tfoperator"] = openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type: []string{"object"},
				Properties: map[string]openapi_spec.Schema{
					"provisioningState":     *openapi_spec.StringProperty(),
					"tfState":               *openapi_spec.StringProperty(),
					"lastAppliedGeneration": *openapi_spec.StringProperty(),
				},
			},
		}

		// Create a top level schema to represent the resource and add spec and status to it.
		def := openapi_spec.Schema{}
		def.Type = openapi_spec.StringOrArray{"object"}
		// Compose these into a top level object
		def.Properties = map[string]openapi_spec.Schema{
			"spec":   specCRD,
			"status": statusCRD,
		}
		def.Description = resourceName

		resources = append(resources, def)
	}

	// Install all of the resources as CRDs into the cluster
	gvrArray := installCRDs(resources, "azurerm", fmt.Sprintf("v%v", "alpha1"))

	fmt.Printf("Creating CRDs - Done")

	return gvrArray
}

// This walks the schema and adds the fields to spec/status based on whether they're computed or not.
func addBlockToSchema(statusCRD, specCRD *openapi_spec.Schema, block *terraform_schema.Block) {
	// For the attributes in this block
	for attributeName, attribute := range block.Attributes {
		// Computer attributes from Terraform map to the `status` block in K8s CRDS
		if attribute.Computed {
			if attribute.Required {
				// Note the the `required` status is set on the parent field so we have to add this at this stage.
				// Example: { required: ["thing1"], subObj { thing1, thing2, } <- rough sudo code.
				statusCRD.Required = append(statusCRD.Required, attributeName)
			}
			// Add the attribute (think field) from the TF schema to the openAPI Spec
			addAttributeToSchema(statusCRD, attributeName, attribute)
		} else {
			// All other attributes are for the `spec` block
			if attribute.Required {
				specCRD.Required = append(specCRD.Required, attributeName)
			}
			addAttributeToSchema(specCRD, attributeName, attribute)
		}
	}

	// For the attributes in child blocks add sub schemas
	for k, v := range block.BlockTypes {
		var nesting string
		if v.Nesting == configschema.NestingMap || v.Nesting == configschema.NestingGroup || v.Nesting == configschema.NestingSingle {
			nesting = "object"
		} else {
			nesting = "array"
		}
		nestedSpecCRD := &openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type:     openapi_spec.StringOrArray{nesting},
				Required: []string{},
			},
		}
		nestedStatusCRD := &openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type:     openapi_spec.StringOrArray{nesting},
				Required: []string{},
			},
		}
		addBlockToSchema(nestedSpecCRD, nestedStatusCRD, &v.Block)
		if specCRD.Properties == nil {
			specCRD.Properties = map[string]openapi_spec.Schema{}
		}
		if statusCRD.Properties == nil {
			statusCRD.Properties = map[string]openapi_spec.Schema{}
		}
		specCRD.Properties[k] = *nestedSpecCRD
		statusCRD.Properties[k] = *nestedStatusCRD
	}
}

func addAttributeToSchema(schema *openapi_spec.Schema, attributeName string, attribute *terraform_schema.Attribute) {
	// Convert the type from the TF type to the best matching openAPI Type (recursive for complex types)
	property := getSchemaForType(attributeName, &attribute.Type)
	property.Description = attribute.Description
	// Todo Handle other types
	if schema.Properties == nil {
		schema.Properties = map[string]openapi_spec.Schema{}
	}
	schema.Properties[attributeName] = *property
}

func getSchemaForType(name string, item *cty.Type) *openapi_spec.Schema {
	// Bulk of mapping from TF Schema -> OpenAPI Schema here.
	// TF Type reference: https://www.terraform.io/docs/extend/schemas/schema-types.html
	// Open API Type reference: https://swagger.io/docs/specification/data-models/data-types/

	var property *openapi_spec.Schema
	// Handle basic types - string, bool, number
	if item.Equals(cty.String) {
		property = openapi_spec.StringProperty()
	} else if item.Equals(cty.Bool) {
		property = openapi_spec.BoolProperty()
	} else if item.Equals(cty.Number) {
		property = openapi_spec.Float64Property()
		// Handle more complex types - map, set and list
	} else if item.IsMapType() {
		mapType := getSchemaForType(name+"mapType", item.MapElementType())
		property = openapi_spec.MapProperty(mapType)
		property.Items = &openapi_spec.SchemaOrArray{
			Schema: mapType,
		}
	} else if item.IsListType() {
		listType := getSchemaForType(name+"listType", item.ListElementType())
		property = openapi_spec.ArrayProperty(listType)
		property.Items = &openapi_spec.SchemaOrArray{
			Schema: listType,
		}
	} else if item.IsSetType() {
		setType := getSchemaForType(name+"setType", item.SetElementType())
		property = openapi_spec.ArrayProperty(setType)
		property.Items = &openapi_spec.SchemaOrArray{
			Schema: setType,
		}
	} else {
		log.Printf("[Error] Unknown type on attribute. Skipping %v", name)
	}

	return property
}

// Todo: Check if this type already existing of if simpler way to handle tracking both Kind and Resource
type GroupVersionFull struct {
	GroupVersionKind     k8s_schema.GroupVersionKind
	GroupVersionResource k8s_schema.GroupVersionResource
}

// k8s stuff
func installCRDs(resources []openapi_spec.Schema, providerName, providerVersion string) []GroupVersionFull {
	clientConfig := getK8sClientConfig()
	// Tracks all CRDs Installed or preexisting generated from the TF Schema
	gvArray := make([]GroupVersionFull, 0, len(resources))
	// Tracks only CRDS newly installed in this run
	crdsToCheckInstalled := []GroupVersionFull{}

	// create the clientset
	apiextensionsClientSet, err := k8s_apiextensionsclientset.NewForConfig(clientConfig)
	if err != nil {
		panic(err)
	}

	// Only install when required
	// Todo: Limit to only CRDs for the operator
	installedCrds, _ := apiextensionsClientSet.ApiextensionsV1beta1().CustomResourceDefinitions().List(context.TODO(), k8s_metav1.ListOptions{})
	installedCrdsMap := map[string]bool{}
	for _, crd := range installedCrds.Items {
		installedCrdsMap[crd.Name] = true
	}

	for _, resource := range resources {
		data, _ := json.Marshal(resource)

		// K8s uses it's own type system for OpenAPI.
		// To easily convert lets serialize ours and deserialize it as theirs
		var jsonSchemaProps k8s_apiextensionsv1beta1.JSONSchemaProps
		_ = json.Unmarshal(data, &jsonSchemaProps)

		// Create the names for the CRD
		kind := strings.Replace(strings.Replace(resource.Description, "_", "-", -1), "azurerm-", "", -1)
		groupName := providerName + ".tfb.local"
		resource := kind + "s"
		version := providerVersion
		crdName := resource + "." + groupName

		crd := &k8s_apiextensionsv1beta1.CustomResourceDefinition{
			ObjectMeta: k8s_metav1.ObjectMeta{
				Name:      crdName,
				Namespace: "default", // Todo: set namespace appropriately in future...
			},
			Spec: k8s_apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   groupName,
				Version: version,
				Scope:   k8s_apiextensionsv1beta1.NamespaceScoped,
				Names: k8s_apiextensionsv1beta1.CustomResourceDefinitionNames{
					Plural: resource,
					Kind:   kind,
				},
				Validation: &k8s_apiextensionsv1beta1.CustomResourceValidation{
					OpenAPIV3Schema: &jsonSchemaProps,
				},
				Subresources: &k8s_apiextensionsv1beta1.CustomResourceSubresources{
					Status: &k8s_apiextensionsv1beta1.CustomResourceSubresourceStatus{},
				},
			},
		}

		gvFull := GroupVersionFull{
			GroupVersionResource: k8s_schema.GroupVersionResource{
				Group:    groupName,
				Resource: resource,
				Version:  version,
			},
			GroupVersionKind: k8s_schema.GroupVersionKind{
				Group:   groupName,
				Version: version,
				Kind:    kind,
			}}

		// Skip CRD Creation if env set.
		_, skipcreation := os.LookupEnv("SKIP_CRD_CREATION")
		kindAlreadyExists := installedCrdsMap[crdName]
		if skipcreation {
			fmt.Println("SKIP_CRD_CREATION set - skipping CRD creation")
		} else if kindAlreadyExists { // Todo: In future this should also check versions too
			fmt.Println("Skipping CRD creation as kind already exists")
		} else {
			_, err = createCustomResourceDefinition("default", apiextensionsClientSet, crd)
			if err != nil {
				log.Println(err.Error())
				continue
			}
			crdsToCheckInstalled = append(crdsToCheckInstalled, gvFull)
		}

		// If CRD was successful created or already exists then add it to GV array
		gvArray = append(gvArray, gvFull)
	}

	// Check any newly installed CRDs are in correct state
	waitForCRDsToBeInstalled(apiextensionsClientSet, crdsToCheckInstalled)

	return gvArray
}

func createCustomResourceDefinition(namespace string, clientSet k8s_apiextensionsclientset.Interface, crd *k8s_apiextensionsv1beta1.CustomResourceDefinition) (*k8s_apiextensionsv1beta1.CustomResourceDefinition, error) {
	crdName := crd.ObjectMeta.Name
	fmt.Printf("Creating CRD %q... ", crdName)
	_, err := clientSet.ApiextensionsV1beta1().CustomResourceDefinitions().Create(context.TODO(), crd, k8s_metav1.CreateOptions{})
	if err == nil {
		fmt.Println("created")
	} else if k8s_apierrors.IsAlreadyExists(err) {
		fmt.Println("already exists")
	} else {
		fmt.Printf("Failed: %+v\n", err)

		return nil, err
	}

	return crd, nil
}

func waitForCRDsToBeInstalled(clientSet k8s_apiextensionsclientset.Interface, crds []GroupVersionFull) {
	for _, resource := range crds {
		crdName := resource.GroupVersionResource.Resource + "." + resource.GroupVersionResource.Group
		// Wait for CRD creation.
		err := k8s_wait.PollImmediate(5*time.Second, 60*time.Second, func() (bool, error) {
			crd, err := clientSet.ApiextensionsV1beta1().CustomResourceDefinitions().Get(context.TODO(), crdName, k8s_metav1.GetOptions{})
			fmt.Printf("Waiting on CRD creation: %q \n", crdName)
			if err != nil {
				fmt.Printf("Fail to wait for CRD creation: %+v\n", err)

				return false, err
			}
			for _, cond := range crd.Status.Conditions {
				switch cond.Type {
				case k8s_apiextensionsv1beta1.Established:
					if cond.Status == k8s_apiextensionsv1beta1.ConditionTrue {
						fmt.Printf("CRD creation confirmed: %q \n", crdName)
						return true, err
					}
				case k8s_apiextensionsv1beta1.NamesAccepted:
					if cond.Status == k8s_apiextensionsv1beta1.ConditionFalse {
						fmt.Printf("Name conflict while wait for CRD creation: %s, %+v\n", cond.Reason, err)
					}
				}
			}

			return false, err
		})

		// If there is an error, delete the object to keep it clean.
		if err != nil {
			panic(err)
		}
	}
}
