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

func createKubernetesCRDsFromTerraformProvider(terraformProvider *terraform_plugin.GRPCProvider) ([]GroupVersionFull, error) {
	// Status: This runs but very little validation of the outputted openAPI apecs has been done. Bugs are likely

	terraformSchema := terraformProvider.GetSchema()

	// Each resource in terraform becomes a openAPI Schema stored in this array
	openAPIResourceSchemas := []openapi_spec.Schema{}

	// Foreach of the resources create a openAPI spec.
	// 1. Split terraform computed values into `status` of the CRD as these are unsettable by user
	// 2. Put required and optional params into the `spec` of the CRD. Setting required status accordinly.
	for terraformResName, terraformRes := range terraformSchema.ResourceTypes {
		// Skip any resources which aren't valid DNS names as they're too long
		if len(terraformResName) > 63 {
			fmt.Printf("Skipping invalid resource - name too long %q", terraformResName)
			continue
		}

		// Create objects for both the spec and status blocks
		specOpenAPISchema := openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type:     openapi_spec.StringOrArray{OpenAPIObjectType},
				Required: []string{},
			},
		}
		// statusCRD is ONLY set for documentation purposes and is not currently enforced in reconciliation
		statusOpenAPISchema := openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type:     openapi_spec.StringOrArray{OpenAPIObjectType},
				Required: []string{},
			},
		}

		// Given the schema block for the tf resources add the properties to
		// either spec or status objects created above
		err := mapTerraformBlockToOpenAPISchema(&statusOpenAPISchema, &specOpenAPISchema, terraformRes.Block)
		if err != nil {
			return nil, err
		}

		// Add tf operator property
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

		// Create a top level schema to represent the resource and add spec and status to it.
		rootOpenAPISchema := openapi_spec.Schema{}
		rootOpenAPISchema.Type = openapi_spec.StringOrArray{OpenAPIObjectType}
		rootOpenAPISchema.Properties = map[string]openapi_spec.Schema{
			"spec":   specOpenAPISchema,
			"status": statusOpenAPISchema,
		}
		rootOpenAPISchema.Description = terraformResName

		openAPIResourceSchemas = append(openAPIResourceSchemas, rootOpenAPISchema)
	}

	// Install all of the resources as CRDs into the cluster
	gvrArray := installOpenAPISchemasAsKubernetesCRDs(openAPIResourceSchemas, "azurerm", fmt.Sprintf("v%v", "alpha1"))

	fmt.Printf("Creating CRDs - Done")

	return gvrArray, nil
}

// This walks the schema and adds the fields to spec/status based on whether they're computed or not.
func mapTerraformBlockToOpenAPISchema(statusOpenAPISchema, specOpenAPISchema *openapi_spec.Schema, terraformBlock *terraform_schema.Block) error {
	for terraformAttrName, terraformAttr := range terraformBlock.Attributes {
		// Computer attributes from Terraform map to the `status` block in K8s CRDS
		if terraformAttr.Computed {
			if terraformAttr.Required {
				// Note the the `required` status is set on the parent field so we have to add this at this stage.
				// Example: { required: ["thing1"], subObj { thing1, thing2, } <- rough sudo code.
				statusOpenAPISchema.Required = append(statusOpenAPISchema.Required, terraformAttrName)
			}
			// Add the attribute (think field) from the TF schema to the openAPI Spec
			err := mapTerraformAttributeToOpenAPISchema(statusOpenAPISchema, terraformAttrName, terraformAttr)
			if err != nil {
				return err
			}
		} else {
			// All other attributes are for the `spec` block
			if terraformAttr.Required {
				specOpenAPISchema.Required = append(specOpenAPISchema.Required, terraformAttrName)
			}
			err := mapTerraformAttributeToOpenAPISchema(specOpenAPISchema, terraformAttrName, terraformAttr)
			if err != nil {
				return err
			}
		}
	}

	// For the attributes in nested blocks add sub schemas
	for nestedTerraformBlockKey, nestedTerraformBlockVal := range terraformBlock.BlockTypes {
		isNestedObject := (nestedTerraformBlockVal.Nesting == configschema.NestingGroup || nestedTerraformBlockVal.Nesting == configschema.NestingSingle)
		nestedSpecOpenAPISchema := &openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type:     openapi_spec.StringOrArray{OpenAPIObjectType},
				Required: []string{},
			},
		}
		nestedStatusOpenAPISchema := &openapi_spec.Schema{
			SchemaProps: openapi_spec.SchemaProps{
				Type:     openapi_spec.StringOrArray{OpenAPIObjectType},
				Required: []string{},
			},
		}
		err := mapTerraformBlockToOpenAPISchema(nestedStatusOpenAPISchema, nestedSpecOpenAPISchema, &nestedTerraformBlockVal.Block)
		if err != nil {
			return err
		}
		if specOpenAPISchema.Properties == nil {
			specOpenAPISchema.Properties = map[string]openapi_spec.Schema{}
		}
		if statusOpenAPISchema.Properties == nil {
			statusOpenAPISchema.Properties = map[string]openapi_spec.Schema{}
		}
		if isNestedObject {
			specOpenAPISchema.Properties[nestedTerraformBlockKey] = *nestedSpecOpenAPISchema
			statusOpenAPISchema.Properties[nestedTerraformBlockKey] = *nestedStatusOpenAPISchema
		} else { // isNestedArray
			specOpenAPISchema.Properties[nestedTerraformBlockKey] = openapi_spec.Schema{
				SchemaProps: openapi_spec.SchemaProps{
					Type: openapi_spec.StringOrArray{OpenAPIArrayType},
					Items: &openapi_spec.SchemaOrArray{
						Schema: nestedSpecOpenAPISchema,
					},
				},
			}
			statusOpenAPISchema.Properties[nestedTerraformBlockKey] = openapi_spec.Schema{
				SchemaProps: openapi_spec.SchemaProps{
					Type: openapi_spec.StringOrArray{OpenAPIArrayType},
					Items: &openapi_spec.SchemaOrArray{
						Schema: nestedStatusOpenAPISchema,
					},
				},
			}
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

	// Convert the type from the TF type to the best matching openAPI Type (recursive for complex types)
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

func getOpenAPISchemaFromTerraformType(terraformAttrName string, terraformAttrType *cty.Type) (*openapi_spec.Schema, error) {
	// Bulk of mapping from TF Schema -> OpenAPI Schema here.
	// TF Type reference: https://www.terraform.io/docs/extend/schemas/schema-types.html
	// Open API Type reference: https://swagger.io/docs/specification/data-models/data-types/
	if terraformAttrType == nil {
		return nil, fmt.Errorf("cannot get openapi schema for nil terraform attribute %s type", terraformAttrName)
	}

	var openAPIPropertySchema *openapi_spec.Schema
	// Handle basic types - string, bool, number
	if terraformAttrType.Equals(cty.String) {
		openAPIPropertySchema = openapi_spec.StringProperty()
	} else if terraformAttrType.Equals(cty.Bool) {
		openAPIPropertySchema = openapi_spec.BoolProperty()
	} else if terraformAttrType.Equals(cty.Number) {
		openAPIPropertySchema = openapi_spec.Float64Property()
		// Handle more complex types - map, set and list
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

// Todo: Check if this type already existing of if simpler way to handle tracking both Kind and Resource
type GroupVersionFull struct {
	GroupVersionKind     k8s_schema.GroupVersionKind
	GroupVersionResource k8s_schema.GroupVersionResource
}

// k8s stuff
func installOpenAPISchemasAsKubernetesCRDs(openAPIResources []openapi_spec.Schema, terraformProviderName, terraformProviderVersion string) []GroupVersionFull {
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
