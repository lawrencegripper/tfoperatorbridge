package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-openapi/spec"
	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/plugin"
	"github.com/zclconf/go-cty/cty"

	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/tools/clientcmd"
)

func createCRDsForResources(provider *plugin.GRPCProvider) {

	tfSchema := provider.GetSchema()

	resources := []spec.Schema{}
	objType := spec.StringOrArray{"object"}

	for resourceName, resource := range tfSchema.ResourceTypes {

		// Create objects for both the spec and status blocks
		specCRD := spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type:     objType,
				Required: []string{},
			},
		}
		statusCRD := spec.Schema{
			SchemaProps: spec.SchemaProps{
				Type:     objType,
				Required: []string{},
			},
		}

		addBlockToSchema(&statusCRD, &specCRD, "root", resource.Block)

		def := spec.Schema{}
		def.Type = objType
		// Compose these into a top level object
		def.Properties = map[string]spec.Schema{
			"spec":   specCRD,
			"status": statusCRD,
		}
		def.Description = resourceName

		resources = append(resources, def)
	}

	// Log out schemas for viewing
	// for _, resource := range resources {
	// 	output, err := json.MarshalIndent(resource, "", "  ")
	// 	if err != nil {
	// 		panic(err)
	// 	}
	//  fmt.Printf("Schema: %+v", string(output))
	// }

	installCRDs(resources, "azurerm", fmt.Sprintf("v%v", "0.1-todo"))

}

func installCRDs(resources []spec.Schema, providerName, providerVersion string) {
	var kubeconfig *string
	if home := homeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}

	// use the current context in kubeconfig
	clientConfig, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// create the clientset
	apiextensionsClientSet, err := apiextensionsclientset.NewForConfig(clientConfig)
	if err != nil {
		panic(err)
	}

	for _, resource := range resources {
		data, err := json.Marshal(resource)

		var jsonSchemaProps apiextensionsv1beta1.JSONSchemaProps
		err = json.Unmarshal(data, &jsonSchemaProps)

		kind := strings.Replace(strings.Replace(resource.Description, "_", "-", -1), "azurerm-", "", -1)
		groupName := providerName + ".tfb.local"
		plural := kind + "s"
		version := providerVersion
		crdName := plural + "." + groupName

		crd := &apiextensionsv1beta1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name:      crdName,
				Namespace: "default",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   groupName,
				Version: version,
				Scope:   apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Plural: plural,
					Kind:   kind,
				},
				Validation: &apiextensionsv1beta1.CustomResourceValidation{
					OpenAPIV3Schema: &jsonSchemaProps,
				},
			},
		}
		_, err = createCustomResourceDefinition("default", apiextensionsClientSet, crd)
		if err != nil {
			log.Println(err.Error())
		}
	}
}

func addBlockToSchema(statusCRD, specCRD *spec.Schema, blockName string, block *configschema.Block) {
	for attributeName, attribute := range block.Attributes {
		// Computer attributes from Terraform map to the `status` block in K8s CRDS
		if attribute.Computed {
			if attribute.Required {
				statusCRD.Required = append(statusCRD.Required, attributeName)
			}
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

// k8s stuff

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func createCustomResourceDefinition(namespace string, clientSet apiextensionsclientset.Interface, crd *apiextensionsv1beta1.CustomResourceDefinition) (*apiextensionsv1beta1.CustomResourceDefinition, error) {
	_, err := clientSet.ApiextensionsV1beta1().CustomResourceDefinitions().Create(context.TODO(), crd, metav1.CreateOptions{})
	if err == nil {
		fmt.Println("CRD created")
	} else if apierrors.IsAlreadyExists(err) {
		fmt.Println("CRD already exists")
	} else {
		fmt.Printf("Fail to create CRD: %+v\n", err)

		return nil, err
	}

	// // Wait for CRD creation.
	// err = wait.Poll(5*time.Second, 60*time.Second, func() (bool, error) {
	// 	crd, err = clientSet.ApiextensionsV1beta1().CustomResourceDefinitions().Get(context.TODO(), crd.Name, metav1.GetOptions{})
	// 	if err != nil {
	// 		fmt.Printf("Fail to wait for CRD creation: %+v\n", err)

	// 		return false, err
	// 	}
	// 	for _, cond := range crd.Status.Conditions {
	// 		switch cond.Type {
	// 		case apiextensionsv1beta1.Established:
	// 			if cond.Status == apiextensionsv1beta1.ConditionTrue {
	// 				return true, err
	// 			}
	// 		case apiextensionsv1beta1.NamesAccepted:
	// 			if cond.Status == apiextensionsv1beta1.ConditionFalse {
	// 				fmt.Printf("Name conflict while wait for CRD creation: %s, %+v\n", cond.Reason, err)
	// 			}
	// 		}
	// 	}

	// 	return false, err
	// })

	// // If there is an error, delete the object to keep it clean.
	// if err != nil {
	// 	fmt.Println("Try to cleanup")
	// 	deleteErr := clientSet.ApiextensionsV1beta1().CustomResourceDefinitions().Delete(context.TODO(), crd.Name, metav1.DeleteOptions{})
	// 	if deleteErr != nil {
	// 		fmt.Printf("Fail to delete CRD: %+v\n", deleteErr)

	// 		return nil, errors.NewAggregate([]error{err, deleteErr})
	// 	}

	// 	return nil, err
	// }

	return crd, nil
}
