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

const (
	OpenAPIObjectType = "object"
	OpenAPIArrayType  = "array"
)

func createK8sCRDsFromTerraformProvider(terraformProvider *terraform_plugin.GRPCProvider) ([]GroupVersionFull, []openapi_spec.Schema, error) {
	// Status: This runs but very little validation of the outputted openAPI apecs has been done. Bugs are likely

	terraformProviderSchema := terraformProvider.GetSchema()

	// Each resource in the terraform providers becomes a OpenAPI schema stored in this array
	openAPIResourceSchemas := []openapi_spec.Schema{}

	// Foreach of the resources create a openAPI spec.
	// 1. Split terraform computed values into `status` of the CRD as these are unsettable by user
	// 2. Put required and optional params into the `spec` of the CRD. Setting required status accordinly.
	for terraformResName, terraformRes := range terraformProviderSchema.ResourceTypes {
		// Skip any resources which aren't valid DNS names as they're too long
		if len(terraformResName) > 63 {
			fmt.Printf("Skipping invalid resource - name too long %q", terraformResName)
			continue
		}

		// TODO: Remove before merge!!
		if terraformResName != "azurerm_storage_account" && terraformResName != "azurerm_resource_group" {
			fmt.Printf("Skipping %q\n", terraformResName)
			continue
		}

		// Create an OpenAPI schema objects for the `status` values in the Kubernetes CRD
		statusOpenAPISchema := openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type:     openapi_spec.StringOrArray{OpenAPIObjectType},
				Required: []string{},
			},
		}
		// Create an OpenAPI schema objects for the `spec` values in the Kubernetes CRD
		specOpenAPISchema := openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type:     openapi_spec.StringOrArray{OpenAPIObjectType},
				Required: []string{},
			},
		}

		// Map the terraform resource's schema block, map attributes and blocks across to the status and spec OpenAPI schemas
		err := mapTerraformBlockToOpenAPISchema(&statusOpenAPISchema, &specOpenAPISchema, terraformRes.Block)
		if err != nil {
			return nil, nil, err
		}

		// Add terraform operator property to store useful metadata used by the operator
		statusOpenAPISchema.Properties["_tfoperator"] = openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type: []string{OpenAPIObjectType},
				Properties: map[string]openapi_spec.Schema{
					"provisioningState":     *openapi_spec.StringProperty(),
					"tfState":               *openapi_spec.StringProperty(),
					"lastAppliedGeneration": *openapi_spec.StringProperty(),
				},
			},
		}

		// Create a root OpenAPI schema containing the spec and status schemas as properties
		rootOpenAPISchema := getRootOpenAPISchema(terraformResName, &statusOpenAPISchema, &specOpenAPISchema)

		openAPIResourceSchemas = append(openAPIResourceSchemas, rootOpenAPISchema)
	}

	// Install all of the resources as CRDs into the cluster
	gvrArray := installOpenAPISchemasAsK8sCRDs(openAPIResourceSchemas, "azurerm", fmt.Sprintf("v%v", "alpha1"))

	fmt.Printf("Creating CRDs - Done")

	return gvrArray, openAPIResourceSchemas, nil
}

func mapTerraformBlockToOpenAPISchema(statusOpenAPISchema, specOpenAPISchema *openapi_spec.Schema, terraformBlock *terraform_schema.Block) error {
	// Traverse all attributes in the terraform block and map them to a property in an OpenAPI schema
	for terraformAttrName, terraformAttr := range terraformBlock.Attributes {
		// Terraform attributes marked as Computed should be added to the status OpenAPI schema.
		// Terraform attributes marked as not Computed should be added to the spec OpenAPI schema.
		shouldAddToStatusOpenAPISchema := terraformAttr.Computed
		// Terraform attributes marked as Required should be added as required in the OpenAPI schema.
		// NOTE: The required field should be set on the parent schema property.
		isRequired := terraformAttr.Required

		if shouldAddToStatusOpenAPISchema {
			if isRequired {
				statusOpenAPISchema.Required = append(statusOpenAPISchema.Required, terraformAttrName)
			}
			err := mapTerraformAttributeToOpenAPISchema(statusOpenAPISchema, terraformAttrName, terraformAttr)
			if err != nil {
				return err
			}
		} else { // should add to spec OpenAPI schema
			if terraformAttr.Required {
				specOpenAPISchema.Required = append(specOpenAPISchema.Required, terraformAttrName)
			}
			err := mapTerraformAttributeToOpenAPISchema(specOpenAPISchema, terraformAttrName, terraformAttr)
			if err != nil {
				return err
			}
		}
	}

	// Traverse all nested terraform blocks in the current terraform block and map them to a property in an OpenAPI schema
	for nestedTerraformBlockKey, nestedTerraformBlockVal := range terraformBlock.BlockTypes {
		// All terraform blocks are mapped to object properties in OpenAPI schema
		nestedStatusOpenAPISchema := getOpenAPIObjectSchema()
		nestedSpecOpenAPISchema := getOpenAPIObjectSchema()
		err := mapTerraformBlockToOpenAPISchema(&nestedStatusOpenAPISchema, &nestedSpecOpenAPISchema, &nestedTerraformBlockVal.Block)
		if err != nil {
			return err
		}
		// We can't differentiate blocks as Computed so all blocks are added to both the spec and status OpenAPI schema
		if statusOpenAPISchema.Properties == nil {
			statusOpenAPISchema.Properties = map[string]openapi_spec.Schema{}
		}
		if specOpenAPISchema.Properties == nil {
			specOpenAPISchema.Properties = map[string]openapi_spec.Schema{}
		}
		// If the minimum items is greater than one then the block is required
		if IsTerraformBlockRequired(nestedTerraformBlockVal) {
			specOpenAPISchema.Required = append(specOpenAPISchema.Required, nestedTerraformBlockKey)
			statusOpenAPISchema.Required = append(statusOpenAPISchema.Required, nestedTerraformBlockKey)
		}
		if IsTerraformNestedBlockAOpenAPIObjectProperty(nestedTerraformBlockVal) {
			// For nested objects, assign directly to a property.
			// This will flatten collections with maxItem <= 1
			statusOpenAPISchema.Properties[nestedTerraformBlockKey] = nestedStatusOpenAPISchema
			specOpenAPISchema.Properties[nestedTerraformBlockKey] = nestedSpecOpenAPISchema
		} else {
			// For nested arrays, wrap the objects in an array property
			statusOpenAPISchema.Properties[nestedTerraformBlockKey] = getOpenAPIArraySchema(nestedStatusOpenAPISchema)
			specOpenAPISchema.Properties[nestedTerraformBlockKey] = getOpenAPIArraySchema(nestedSpecOpenAPISchema)
		}
	}
	return nil
}

func mapTerraformAttributeToOpenAPISchema(parentOpenAPISchema *openapi_spec.Schema, terraformAttrName string, terraformAttr *terraform_schema.Attribute) error {
	if parentOpenAPISchema == nil {
		return fmt.Errorf("cannot map terraform attribute %s to nil openapi schema", terraformAttrName)
	}
	if terraformAttr == nil {
		return fmt.Errorf("cannot map nil terraform attribute %s to openapi schema", terraformAttrName)
	}

	openAPISchema, err := getOpenAPISchemaFromTerraformType(terraformAttrName, &terraformAttr.Type)
	if err != nil {
		return err
	}
	if openAPISchema == nil {
		log.Printf("Terraform attribute of type %+v is empty, Skipping %s", terraformAttr.Type, terraformAttrName)
		return nil
	}
	openAPISchema.Description = terraformAttr.Description

	if parentOpenAPISchema.Properties == nil {
		parentOpenAPISchema.Properties = map[string]openapi_spec.Schema{}
	}
	parentOpenAPISchema.Properties[terraformAttrName] = *openAPISchema
	return nil
}

// getOpenAPISchemaFromTerraformType returns the an OpenAPI schema representing the most suitable type for the
// provided terraform attribute type. Returns an error if the attribute type is missing or a suitable type
// cannot be determined.
// References:
// - terraform schema types: https://www.terraform.io/docs/extend/schemas/schema-types.html
// - openapi schema types: https://swagger.io/docs/specification/data-models/data-types/
func getOpenAPISchemaFromTerraformType(terraformAttrName string, terraformAttrType *cty.Type) (*openapi_spec.Schema, error) {
	if terraformAttrType == nil {
		return nil, fmt.Errorf("cannot get openapi schema for nil terraform attribute %s type", terraformAttrName)
	}
	var openAPIPropertySchema *openapi_spec.Schema
	if terraformAttrType.Equals(cty.String) {
		openAPIPropertySchema = openapi_spec.StringProperty()
	} else if terraformAttrType.Equals(cty.Bool) {
		openAPIPropertySchema = openapi_spec.BoolProperty()
	} else if terraformAttrType.Equals(cty.Number) {
		openAPIPropertySchema = openapi_spec.Float64Property()
	} else if terraformAttrType.IsMapType() {
		mapType, err := getOpenAPISchemaFromTerraformType(terraformAttrName+"mapType", terraformAttrType.MapElementType())
		if err != nil {
			return nil, err
		}
		openAPIPropertySchema = openapi_spec.MapProperty(mapType)
	} else if terraformAttrType.IsListType() {
		listType, err := getOpenAPISchemaFromTerraformType(terraformAttrName+"listType", terraformAttrType.ListElementType())
		if err != nil {
			return nil, err
		}
		openAPIPropertySchema = openapi_spec.ArrayProperty(listType)
	} else if terraformAttrType.IsSetType() {
		setType, err := getOpenAPISchemaFromTerraformType(terraformAttrName+"setType", terraformAttrType.SetElementType())
		if err != nil {
			return nil, err
		}
		openAPIPropertySchema = openapi_spec.ArrayProperty(setType)
	} else if terraformAttrType.IsObjectType() {
		openAPISchemaType := openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type: openapi_spec.StringOrArray{OpenAPIObjectType},
			},
		}
		isEmptyObject := (len(terraformAttrType.AttributeTypes()) == 0)
		if isEmptyObject {
			return nil, nil // Ignore empty objects
		}
		for terraformObjAttrTypeKey, terraformObjAttrTypeVal := range terraformAttrType.AttributeTypes() {
			openAPIAttrSchema, err := getOpenAPISchemaFromTerraformType(terraformAttrName+terraformObjAttrTypeKey, &terraformObjAttrTypeVal)
			if err != nil {
				return nil, err
			}
			if openAPISchemaType.Properties == nil {
				openAPISchemaType.Properties = map[string]openapi_spec.Schema{}
			}
			openAPISchemaType.Properties[terraformObjAttrTypeKey] = *openAPIAttrSchema
		}
		openAPIPropertySchema = &openAPISchemaType
	} else {
		return nil, fmt.Errorf("unknown terraform attribute %s of type %+v", terraformAttrType, terraformAttrName)
	}

	return openAPIPropertySchema, nil
}

// GroupVersionFull provides both the groupVersionKind and groupVersionResource representations of the CRD. Todo: Investigate if existing types can supply this.
type GroupVersionFull struct {
	GroupVersionKind     k8s_schema.GroupVersionKind
	GroupVersionResource k8s_schema.GroupVersionResource
}

// k8s stuff
func installOpenAPISchemasAsK8sCRDs(openAPIResources []openapi_spec.Schema, terraformProviderName, terraformProviderVersion string) []GroupVersionFull {
	clientConfig := getK8sClientConfig()
	// Tracks all CRDs Installed or preexisting generated from the TF Schema
	gvArray := make([]GroupVersionFull, 0, len(openAPIResources))
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

	for _, resource := range openAPIResources {
		data, _ := json.Marshal(resource)

		// K8s uses it's own type system for OpenAPI.
		// To easily convert lets serialize ours and deserialize it as theirs
		var jsonSchemaProps k8s_apiextensionsv1beta1.JSONSchemaProps
		_ = json.Unmarshal(data, &jsonSchemaProps)

		// Create the names for the CRD
		kind := strings.Replace(strings.Replace(resource.Description, "_", "-", -1), "azurerm-", "", -1)
		groupName := terraformProviderName + ".tfb.local"
		resource := kind + "s"
		version := terraformProviderVersion
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

func getRootOpenAPISchema(name string, status *openapi_spec.Schema, spec *openapi_spec.Schema) openapi_spec.Schema {
	// Returns the root openapi schema containing both the `status` and `spec` sub schemas
	return openapi_spec.Schema{
		SchemaProps: openapi_spec.SchemaProps{
			Type: openapi_spec.StringOrArray{OpenAPIObjectType},
			Properties: map[string]openapi_spec.Schema{
				"spec":   *spec,
				"status": *status,
			},
			Description: name,
		},
	}
}

func getOpenAPIObjectSchema() openapi_spec.Schema {
	// Returns an object openapi schema
	return openapi_spec.Schema{
		SchemaProps: openapi_spec.SchemaProps{
			Type:     openapi_spec.StringOrArray{OpenAPIObjectType},
			Required: []string{},
		},
	}
}

func getOpenAPIArraySchema(itemSchema openapi_spec.Schema) openapi_spec.Schema {
	// Returns a openapi schema wrapped in an openapi array property schema
	return openapi_spec.Schema{
		SchemaProps: openapi_spec.SchemaProps{
			Type: openapi_spec.StringOrArray{OpenAPIArrayType},
			Items: &openapi_spec.SchemaOrArray{
				Schema: &itemSchema,
			},
		},
	}
}

func IsTerraformNestedBlockAOpenAPIObjectProperty(nestedBlock *configschema.NestedBlock) bool {
	if nestedBlock == nil {
		return false
	}
	// Does the nesting mode or the max/min items indicate this
	// should map to a nested object in the openapi schema
	return (nestedBlock.Nesting == configschema.NestingGroup ||
		nestedBlock.Nesting == configschema.NestingSingle ||
		(nestedBlock.MaxItems == 1 && nestedBlock.MinItems <= 1))
}

func IsTerraformBlockRequired(nestedBlock *configschema.NestedBlock) bool {
	if nestedBlock == nil {
		return false
	}
	// Is atleast one instance of the block required
	return nestedBlock.MinItems >= 1
}
