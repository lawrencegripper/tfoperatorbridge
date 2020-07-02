package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/go-openapi/spec"
	"github.com/hashicorp/terraform/configs/configschema"
	"github.com/hashicorp/terraform/plugin"
	"github.com/hashicorp/terraform/plugin/discovery"
	"github.com/zclconf/go-cty/cty"

	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	pluginMeta := discovery.FindPlugins(plugin.ProviderPluginName, []string{"./hack/.terraform/plugins/linux_amd64/"}).WithName("azurerm")

	if pluginMeta.Count() < 1 {
		panic("no plugins found")
	}
	pluginClient := plugin.Client(pluginMeta.Newest())
	rpcClient, err := pluginClient.Client()
	if err != nil {
		panic(fmt.Errorf("Failed to initialize plugin: %s", err))
	}
	// create a new resource provisioner.
	raw, err := rpcClient.Dispense(plugin.ProviderPluginName)
	if err != nil {
		panic(fmt.Errorf("Failed to dispense plugin: %s", err))
	}

	provider := raw.(*plugin.GRPCProvider)

	tfSchema := provider.GetSchema()
	// fmt.Printf("provider schemea: %+v", tfSchema)

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

	// Get schema

	for _, resource := range resources {
		output, err := json.MarshalIndent(resource, "", "  ")
		if err != nil {
			panic(err)
		}
		fmt.Printf("Schema: %+v", string(output))
	}

	installCRDs(resources)

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

func installCRDs(resources []spec.Schema) {
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

	CreateCustomResourceDefinition("default", apiextensionsClientSet)
}

const (
	// GroupName is the group name used in this package.
	GroupName string = "tfbridge"
	Kind      string = "tbd"
	// GroupVersion is the version.
	GroupVersion string = "v1"
	Plural       string = "tbds"
	Singular     string = "tbd"
	CRDName      string = Plural + "." + GroupName
)

var (
	// SchemeGroupVersion is the group version used to register these objects.
	SchemeGroupVersion = schema.GroupVersion{
		Group:   GroupName,
		Version: GroupVersion,
	}
	// SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// AddToScheme   = SchemeBuilder.AddToScheme
)

// CreateCustomResourceDefinition creates the CRD and add it into Kubernetes. If there is error,
// it will do some clean up.
func CreateCustomResourceDefinition(namespace string, clientSet apiextensionsclientset.Interface) (*apiextensionsv1beta1.CustomResourceDefinition, error) {
	crd := &apiextensionsv1beta1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      CRDName,
			Namespace: namespace,
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   GroupName,
			Version: SchemeGroupVersion.Version,
			Scope:   apiextensionsv1beta1.NamespaceScoped,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Plural: Plural,
				Kind:   Kind,
			},
		},
	}
	_, err := clientSet.ApiextensionsV1beta1().CustomResourceDefinitions().Create(context.TODO(), crd, metav1.CreateOptions{})
	if err == nil {
		fmt.Println("CRD created")
	} else if apierrors.IsAlreadyExists(err) {
		fmt.Println("CRD already exists")
	} else {
		fmt.Printf("Fail to create CRD: %+v\n", err)

		return nil, err
	}

	// Wait for CRD creation.
	err = wait.Poll(5*time.Second, 60*time.Second, func() (bool, error) {
		crd, err = clientSet.ApiextensionsV1beta1().CustomResourceDefinitions().Get(context.TODO(), CRDName, metav1.GetOptions{})
		if err != nil {
			fmt.Printf("Fail to wait for CRD creation: %+v\n", err)

			return false, err
		}
		for _, cond := range crd.Status.Conditions {
			switch cond.Type {
			case apiextensionsv1beta1.Established:
				if cond.Status == apiextensionsv1beta1.ConditionTrue {
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
		fmt.Println("Try to cleanup")
		deleteErr := clientSet.ApiextensionsV1beta1().CustomResourceDefinitions().Delete(context.TODO(), CRDName, metav1.DeleteOptions{})
		if deleteErr != nil {
			fmt.Printf("Fail to delete CRD: %+v\n", deleteErr)

			return nil, errors.NewAggregate([]error{err, deleteErr})
		}

		return nil, err
	}

	return crd, nil
}
