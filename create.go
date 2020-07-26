package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/go-openapi/spec"
	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/plugin"
	"github.com/zclconf/go-cty/cty"

	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

func createCRDsForResources(provider *plugin.GRPCProvider) []GroupVersionFull {
	// Status: This runs but very little validation of the outputted openAPI apecs has been done. Bugs are likely

	tfSchema := provider.GetSchema()

	// Each resource in terraform becomes a openAPI Schema stored in this array
	resources := []spec.Schema{}

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
		specCRD := spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type:     spec.StringOrArray{"object"},
				Required: []string{},
			},
		}
		statusCRD := spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type:     spec.StringOrArray{"object"},
				Required: []string{},
			},
		}

		// Given the schema block for the tf resources add the properties to
		// either spec or status objects created above
		addBlockToSchema(&statusCRD, &specCRD, "root", resource.Block)

		// Add tf operator property
		statusCRD.Properties["_tfoperator"] = spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type: []string{"object"},
				Properties: map[string]spec.Schema{
					"provisioningState":     *spec.StringProperty(),
					"tfState":               *spec.StringProperty(),
					"lastAppliedGeneration": *spec.StringProperty(),
				},
			},
		}

		// Create a top level schema to represent the resource and add spec and status to it.
		def := spec.Schema{}
		def.Type = spec.StringOrArray{"object"}
		// Compose these into a top level object
		def.Properties = map[string]spec.Schema{
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
func addBlockToSchema(statusCRD, specCRD *spec.Schema, blockName string, block *configschema.Block) {
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
}

func addAttributeToSchema(schema *spec.Schema, attributeName string, attribute *configschema.Attribute) {
	// Convert the type from the TF type to the best matching openAPI Type (recursive for complex types)
	property := getSchemaForType(attributeName, &attribute.Type)
	property.Description = attribute.Description
	// Todo Handle other types
	if schema.Properties == nil {
		schema.Properties = map[string]spec.Schema{}
	}
	schema.Properties[attributeName] = *property
}

func getSchemaForType(name string, item *cty.Type) *spec.Schema {
	// Bulk of mapping from TF Schema -> OpenAPI Schema here.
	// TF Type reference: https://www.terraform.io/docs/extend/schemas/schema-types.html
	// Open API Type reference: https://swagger.io/docs/specification/data-models/data-types/

	var property *spec.Schema
	// Handle basic types - string, bool, number
	if item.Equals(cty.String) {
		property = spec.StringProperty()
	} else if item.Equals(cty.Bool) {
		property = spec.BoolProperty()
	} else if item.Equals(cty.Number) {
		property = spec.Float64Property()

		// Handle more complex types - map, set and list
	} else if item.IsMapType() {
		mapType := getSchemaForType(name+"mapType", item.MapElementType())
		property = spec.MapProperty(mapType)
	} else if item.IsListType() {
		listType := getSchemaForType(name+"listType", item.ListElementType())
		property = spec.ArrayProperty(listType)
	} else if item.IsSetType() {
		setType := getSchemaForType(name+"setType", item.SetElementType())
		property = spec.ArrayProperty(setType)
	} else {
		log.Printf("[Error] Unknown type on attribute. Skipping %v", name)
	}

	return property
}

// Todo: Check if this type already existing of if simplier way to handle tracking both Kind and Resource
type GroupVersionFull struct {
	GroupVersionKind     schema.GroupVersionKind
	GroupVersionResource schema.GroupVersionResource
}

// k8s stuff
func installCRDs(resources []spec.Schema, providerName, providerVersion string) []GroupVersionFull {
	clientConfig := getK8sClientConfig()
	// Tracks all CRDs Installed or preexisting generated from the TF Schema
	gvArray := make([]GroupVersionFull, 0, len(resources))
	// Tracks only CRDS newly installed in this run
	crdsToCheckInstalled := []GroupVersionFull{}

	// create the clientset
	apiextensionsClientSet, err := apiextensionsclientset.NewForConfig(clientConfig)
	if err != nil {
		panic(err)
	}

	// Only install when required
	// Todo: Limit to only CRDs for the operator
	installedCrds, err := apiextensionsClientSet.ApiextensionsV1beta1().CustomResourceDefinitions().List(context.TODO(), metav1.ListOptions{})
	installedCrdsMap := map[string]bool{}
	for _, crd := range installedCrds.Items {
		installedCrdsMap[crd.Name] = true
	}

	for _, resource := range resources {
		data, err := json.Marshal(resource)

		// K8s uses it's own type system for OpenAPI.
		// To easily convert lets serialize ours and deserialize it as theirs
		var jsonSchemaProps apiextensionsv1beta1.JSONSchemaProps
		err = json.Unmarshal(data, &jsonSchemaProps)

		// Create the names for the CRD
		kind := strings.Replace(strings.Replace(resource.Description, "_", "-", -1), "azurerm-", "", -1)
		groupName := providerName + ".tfb.local"
		resource := kind + "s"
		version := providerVersion
		crdName := resource + "." + groupName

		crd := &apiextensionsv1beta1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      crdName,
				Namespace: "default", // Todo: set namespace appropriately in future...
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   groupName,
				Version: version,
				Scope:   apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Plural: resource,
					Kind:   kind,
				},
				Validation: &apiextensionsv1beta1.CustomResourceValidation{
					OpenAPIV3Schema: &jsonSchemaProps,
				},
				Subresources: &apiextensionsv1beta1.CustomResourceSubresources{
					Status: &apiextensionsv1beta1.CustomResourceSubresourceStatus{},
				},
			},
		}

		gvFull := GroupVersionFull{
			GroupVersionResource: schema.GroupVersionResource{
				Group:    groupName,
				Resource: resource,
				Version:  version,
			},
			GroupVersionKind: schema.GroupVersionKind{
				Group:   groupName,
				Version: version,
				Kind:    kind,
			}}

		// Skip CRD Creation if env set.
		_, skipcreation := os.LookupEnv("SKIP_CRD_CREATION")
		kindAlreadyExists, _ := installedCrdsMap[crdName]
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

		// If CRD was successfull created or already exists then add it to GV array
		gvArray = append(gvArray, gvFull)
	}

	// Check any newly installed CRDs are in correct state
	waitForCRDsToBeInstalled(apiextensionsClientSet, crdsToCheckInstalled)

	return gvArray
}

func createCustomResourceDefinition(namespace string, clientSet apiextensionsclientset.Interface, crd *apiextensionsv1beta1.CustomResourceDefinition) (*apiextensionsv1beta1.CustomResourceDefinition, error) {
	crdName := crd.ObjectMeta.Name
	fmt.Printf("Creating CRD %q... ", crdName)
	_, err := clientSet.ApiextensionsV1beta1().CustomResourceDefinitions().Create(context.TODO(), crd, metav1.CreateOptions{})
	if err == nil {
		fmt.Println("created")
	} else if apierrors.IsAlreadyExists(err) {
		fmt.Println("already exists")
	} else {
		fmt.Printf("Failed: %+v\n", err)

		return nil, err
	}

	return crd, nil
}

func waitForCRDsToBeInstalled(clientSet apiextensionsclientset.Interface, crds []GroupVersionFull) {
	for _, resource := range crds {
		crdName := resource.GroupVersionResource.Resource + "." + resource.GroupVersionResource.Group
		// Wait for CRD creation.
		err := wait.PollImmediate(5*time.Second, 60*time.Second, func() (bool, error) {
			crd, err := clientSet.ApiextensionsV1beta1().CustomResourceDefinitions().Get(context.TODO(), crdName, metav1.GetOptions{})
			fmt.Printf("Waiting on CRD creation: %q \n", crdName)
			if err != nil {
				fmt.Printf("Fail to wait for CRD creation: %+v\n", err)

				return false, err
			}
			for _, cond := range crd.Status.Conditions {
				switch cond.Type {
				case apiextensionsv1beta1.Established:
					if cond.Status == apiextensionsv1beta1.ConditionTrue {
						fmt.Printf("CRD creation confirmed: %q \n", crdName)
						return true, err
					}
				case apiextensionsv1beta1.NamesAccepted:
					if cond.Status == apiextensionsv1beta1.ConditionFalse {
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
